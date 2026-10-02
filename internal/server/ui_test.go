package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func uiRequest(t *testing.T, srv *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: srv.newSession("admin")})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestUIAssetsAreEmbeddedAndVersioned(t *testing.T) {
	for _, name := range []string{"app.css", "app.js"} {
		t.Run(name, func(t *testing.T) {
			path := "/assets/" + name
			rec := httptest.NewRecorder()
			handleUIAsset(rec, httptest.NewRequest(http.MethodGet, path+"?v="+uiAssetVersion, nil))
			if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
				t.Fatalf("missing embedded asset: %d", rec.Code)
			}
			contentType := "text/css; charset=utf-8"
			if name == "app.js" {
				contentType = "text/javascript; charset=utf-8"
			}
			if rec.Header().Get("Content-Type") != contentType || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatalf("unexpected asset headers: %v", rec.Header())
			}
			if rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
				t.Fatal("versioned asset should be cached")
			}
			etag := rec.Header().Get("ETag")
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("If-None-Match", etag)
			revalidated := httptest.NewRecorder()
			handleUIAsset(revalidated, req)
			if revalidated.Code != http.StatusNotModified || revalidated.Body.Len() != 0 {
				t.Fatal("matching ETag should return an empty 304")
			}
			if revalidated.Header().Get("Cache-Control") != "public, max-age=0, must-revalidate" {
				t.Fatal("unversioned assets must revalidate")
			}
			head := httptest.NewRecorder()
			handleUIAsset(head, httptest.NewRequest(http.MethodHead, path, nil))
			if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("ETag") != etag {
				t.Fatal("HEAD must return metadata without a body")
			}
			post := httptest.NewRecorder()
			handleUIAsset(post, httptest.NewRequest(http.MethodPost, path, nil))
			if post.Code != http.StatusMethodNotAllowed {
				t.Fatal("asset mutation should be rejected")
			}
		})
	}
}

func TestNavigationContractAndInitialJSON(t *testing.T) {
	srv, err := New(writeTempConfig(t, authTestConfig()))
	if err != nil {
		t.Fatal(err)
	}
	unauthorized := httptest.NewRecorder()
	srv.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/navigation", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatal("navigation must be protected by authentication")
	}
	if rec := uiRequest(t, srv, http.MethodPost, "/api/navigation", "{}"); rec.Code != http.StatusMethodNotAllowed {
		t.Fatal("navigation endpoint must be read-only")
	}
	cfg := cloneConfig(srv.currentConfig())
	cfg.Groups = append(cfg.Groups, Group{ID: "empty", Name: "空分组"})
	cfg.Groups[0].Services[0].Name = `</script><script>alert("x")</script>`
	srv.mu.Lock()
	srv.cfg = cfg
	srv.mu.Unlock()
	rec := uiRequest(t, srv, http.MethodGet, "/api/navigation", "")
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unexpected navigation response: %d", rec.Code)
	}
	var data navigationData
	if err := json.Unmarshal(rec.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Groups) != 2 || data.Groups[1].Services == nil || len(data.Groups[1].Services) != 0 {
		t.Fatal("empty groups must survive the JSON boundary as empty arrays")
	}
	if data.Groups[0].Services[0].GroupID != "ops" || data.Groups[0].Services[0].Pinned == nil {
		t.Fatal("navigation must include group ownership and explicit pin state")
	}
	for _, sensitive := range []string{"password", "session_secret", "uploads_dir", "icon_cache_dir", "test-password", cfg.Auth.SessionSecret} {
		if strings.Contains(rec.Body.String(), sensitive) {
			t.Fatalf("navigation leaked %q", sensitive)
		}
	}
	index := uiRequest(t, srv, http.MethodGet, "/", "").Body.String()
	start := strings.Index(index, `<script id="navigation-data" type="application/json">`)
	if start < 0 {
		t.Fatal("initial navigation JSON missing")
	}
	initial := strings.SplitN(index[start:], ">", 2)[1]
	initial = strings.SplitN(initial, "</script>", 2)[0]
	var initialData navigationData
	if err := json.Unmarshal([]byte(initial), &initialData); err != nil {
		t.Fatalf("initial data is not safe JSON: %v", err)
	}
	if !reflect.DeepEqual(initialData, data) {
		t.Fatal("initial HTML and refresh endpoint must share the same contract")
	}
	for _, want := range []string{"/assets/app.css?v=" + uiAssetVersion, "/assets/app.js?v=" + uiAssetVersion, `id="empty-state"`, `data-group-id="empty"`} {
		if !strings.Contains(index, want) {
			t.Fatalf("index missing %q", want)
		}
	}
}

func TestPinnedServiceRoundTripAndLegacyUpdate(t *testing.T) {
	path := writeTempConfig(t, sortTestConfig())
	srv, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	base := `{"name":"App","group_id":"ops","internal_url":"http://app.example.local","health":{"type":"disabled"}`
	for _, step := range []struct {
		pinned string
		want   bool
	}{{`,"pinned":true}`, true}, {`}`, true}, {`,"pinned":false}`, false}, {`,"pinned":true}`, true}} {
		rec := uiRequest(t, srv, http.MethodPut, "/api/services/app", base+step.pinned)
		if rec.Code != http.StatusOK {
			t.Fatalf("update failed: %s", rec.Body.String())
		}
		cfg, err := LoadConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Groups[0].Services[0].Pinned != step.want {
			t.Fatalf("pin state mismatch: want %v", step.want)
		}
		var result struct {
			Navigation navigationData `json:"navigation"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(result.Navigation, navigationState(cfg)) {
			t.Fatalf("mutation response must match persisted navigation: got %#v, want %#v", result.Navigation.Groups[0].Services[0], navigationState(cfg).Groups[0].Services[0])
		}
	}
	sort := `{"groups":[{"group_id":"ops","service_ids":["metrics"]},{"group_id":"tools","service_ids":["app","drawio"]}]}`
	if rec := uiRequest(t, srv, http.MethodPut, "/api/services/sort", sort); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Groups[1].Services[0].Pinned {
		t.Fatal("cross-group sorting must retain pin state")
	}
	restarted, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if !restarted.currentConfig().Groups[1].Services[0].Pinned {
		t.Fatal("pin state must survive a server restart")
	}
	created := uiRequest(t, srv, http.MethodPost, "/api/services", `{"name":"New pinned","group_id":"ops","external_url":"https://example.test","pinned":true,"health":{"type":"disabled"}}`)
	if created.Code != http.StatusOK {
		t.Fatal(created.Body.String())
	}
	cfg, err = LoadConfig(path)
	if err != nil || !cfg.Groups[0].Services[1].Pinned {
		t.Fatalf("new pin was not persisted: %v", err)
	}
}

func TestHealthSwitchClearsInactiveFieldsInSavedConfig(t *testing.T) {
	path := writeTempConfig(t, authTestConfig())
	srv, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"http", "tcp", "disabled"} {
		body := `{"name":"App","internal_url":"http://app.example.local","health":{"type":"` + kind + `","url":"http://app.example.local/healthz","address":"app.example.local:443","expect_status":204,"timeout":"2s"}}`
		rec := uiRequest(t, srv, http.MethodPut, "/api/services/app", body)
		if rec.Code != http.StatusOK {
			t.Fatal(rec.Body.String())
		}
		cfg, err := LoadConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		health := cfg.Groups[0].Services[0].Health
		if kind == "http" && (health.URL == "" || health.Address != "" || health.ExpectStatus != 204) {
			t.Fatalf("HTTP fields incorrect: %#v", health)
		}
		if kind == "tcp" && (health.Address == "" || health.URL != "" || health.ExpectStatus != 0) {
			t.Fatalf("TCP retained HTTP fields: %#v", health)
		}
		if kind == "disabled" && (health.Address != "" || health.URL != "" || health.ExpectStatus != 0) {
			t.Fatalf("disabled monitoring retained targets: %#v", health)
		}
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "expect_status:") || strings.Contains(string(content), "address:") || strings.Contains(string(content), "/healthz") {
		t.Fatal("inactive health fields should not be written to YAML")
	}
}

func TestStatusCacheRejectsOutdatedProbeResults(t *testing.T) {
	old := Service{ID: "app", Name: "Old", Health: HealthCheck{Type: "http", URL: "http://old.example.test", ExpectStatus: 200, Timeout: time.Second}}
	cfg := &Config{Groups: []Group{{ID: "ops", Services: []Service{old}}}}
	cache := NewStatusCache(cfg)
	cache.setStatus(old, ServiceStatus{ID: "app", Type: "http", Status: StatusHealthy})
	next := old
	next.Name = "Renamed"
	cfg.Groups[0].Services[0] = next
	cache.UpdateConfig(cfg)
	if status := cache.Snapshot().Services["app"]; status.Status != StatusHealthy || status.Name != "Renamed" {
		t.Fatal("renaming must preserve the check for an unchanged target")
	}
	next.Health.URL = "http://new.example.test"
	cfg.Groups[0].Services[0] = next
	cache.UpdateConfig(cfg)
	cache.setStatus(old, ServiceStatus{ID: "app", Type: "http", Status: StatusHealthy})
	if status := cache.Snapshot().Services["app"]; status.Status != StatusUnknown || status.CheckedAt != nil {
		t.Fatal("changed target must not inherit the old result")
	}
	cache.setStatus(next, ServiceStatus{ID: "app", Type: "http", Status: StatusHealthy})
	if cache.Snapshot().Services["app"].Status != StatusHealthy {
		t.Fatal("current target should accept a check")
	}
	cfg.Groups[0].Services = nil
	cache.UpdateConfig(cfg)
	cache.setStatus(next, ServiceStatus{ID: "app", Type: "http", Status: StatusHealthy})
	if len(cache.Snapshot().Services) != 0 {
		t.Fatal("an in-flight check must not restore a deleted service")
	}
}

func TestUserErrorsHaveFieldHintsWithoutTechnicalDetails(t *testing.T) {
	for _, test := range []struct{ message, field string }{
		{`配置错误: groups[0].services[0]: health.address 必须是 host:port`, "health_address"},
		{`health.timeout 格式无效`, "health_timeout"},
		{`internal_domain_url 仅支持 http/https`, "internal_domain_url"},
		{`name 不能为空`, "name"},
		{`写入临时配置失败: open /private/services.yaml: permission denied`, ""},
		{`读取图库失败: unexpected EOF at /private/uploads`, ""},
		{`unexpected internal failure: /private/config`, ""},
	} {
		message, field := userError(test.message)
		if field != test.field || strings.Contains(message, "/private/") || strings.Contains(message, "health.") || strings.Contains(message, "unexpected") {
			t.Fatalf("unsafe or unhelpful user error: %q, %q", message, field)
		}
	}
}

func TestUploadedNamesRemainSearchableAndSafe(t *testing.T) {
	for _, filename := range []string{"../控制台 图标.png", `C:\private\控制台 图标.png`} {
		first, err := randomUploadName(filename, ".png")
		if err != nil {
			t.Fatal(err)
		}
		second, err := randomUploadName(filename, ".png")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(first, "控制台-图标-") || !strings.HasSuffix(first, ".png") || strings.ContainsAny(first, `/\`) || first == second {
			t.Fatalf("unsafe or unsearchable uploaded name: %q", first)
		}
	}
}
