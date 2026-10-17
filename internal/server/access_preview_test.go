package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
)

func accessPreviewServer(t *testing.T) (*Server, string) {
	t.Helper()
	path := writeTempConfig(t, authTestConfig())
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Access = dynamicAccess()
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	srv, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	return srv, path
}

func TestAccessPreviewUsesSaveRulesWithoutWritingConfig(t *testing.T) {
	srv, path := accessPreviewServer(t)
	before, _ := os.ReadFile(path)
	const raw = "http://192.168.9.25:5001/path?hostname=192.168.9.25&port=9097&note=%2F#view"
	for _, host := range []string{"10.90.1.2", "100.120.8.9", "192.168.10.20"} {
		payload, _ := json.Marshal(serviceAccessRequest{InternalURL: raw, SameHost: true, ExternalURL: "https://app.example.test/"})
		rec := uiRequest(t, srv, http.MethodPost, "http://"+host+":3002/api/navigation/preview", string(payload))
		if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("preview failed: %d %s", rec.Code, rec.Body.String())
		}
		var got serviceAccessPreview
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if !got.SameHost || got.InternalURL != "http://home-lan.example.test:5001/path?hostname=home-lan.example.test&port=9097&note=%2F#view" || got.InternalAccessURL != "http://"+host+":5001/path?hostname="+host+"&port=9097&note=%2F#view" {
			t.Fatalf("preview did not use canonical source and request address: %#v", got)
		}
	}
	for _, tt := range []struct{ raw, host, want string }{
		{"http://home-lan.example.test:8090/", "10.33.4.5", ""},
		{"http://home-lan.example.test:8090/", "100.123.4.5", "http://100.123.4.5:8090/"},
		{"https://other-device.example.test/", "10.33.4.5", "https://other-device.example.test/"},
		{"", "10.33.4.5", ""},
	} {
		payload, _ := json.Marshal(serviceAccessRequest{InternalURL: tt.raw})
		rec := uiRequest(t, srv, http.MethodPost, "http://"+tt.host+"/api/navigation/preview", string(payload))
		var got serviceAccessPreview
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.InternalAccessURL != tt.want {
			t.Fatalf("wrong preview for %s: %s", tt.raw, rec.Body.String())
		}
	}
	request := httptest.NewRequest(http.MethodPost, "http://home.example.test/api/navigation/preview", strings.NewReader(`{"internal_url":"http://home-lan.example.test:5001/"}`))
	request.Header.Set("X-Forwarded-Host", "10.99.1.2")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: srv.newSession("admin")})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, request)
	var got serviceAccessPreview
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.InternalAccessURL != "http://home-lan.example.test:5001/" {
		t.Fatal("preview trusted a forwarded host")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("draft preview changed the persisted configuration")
	}
}

func TestAccessPreviewRejectsUnauthorizedAndInvalidDrafts(t *testing.T) {
	srv, path := accessPreviewServer(t)
	before, _ := os.ReadFile(path)
	unauthorized := httptest.NewRecorder()
	srv.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/api/navigation/preview", strings.NewReader(`{}`)))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatal("preview must require the same authentication as navigation")
	}
	if rec := uiRequest(t, srv, http.MethodGet, "/api/navigation/preview", ""); rec.Code != http.StatusMethodNotAllowed {
		t.Fatal("preview must accept only POST")
	}
	for _, body := range []string{
		`{"internal_url":"javascript:alert(1)","same_host":true}`,
		`{"external_url":"file:///etc/passwd"}`,
		`{"internal_domain_url":"invalid"}`,
		`{"same_host":true}`,
		`{"unknown":"value"}`,
		`{"internal_url":"` + strings.Repeat("a", 33<<10) + `"}`,
	} {
		rec := uiRequest(t, srv, http.MethodPost, "/api/navigation/preview", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid draft accepted: %d", rec.Code)
		}
		for _, secret := range []string{srv.currentConfig().Auth.Password, srv.currentConfig().Auth.SessionSecret} {
			if strings.Contains(rec.Body.String(), secret) {
				t.Fatal("preview error exposed authentication data")
			}
		}
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("rejected preview changed the configuration")
	}
}

func TestSameHostIntentPersistsOnlyStableURL(t *testing.T) {
	srv, path := accessPreviewServer(t)
	access := cloneConfig(srv.currentConfig()).Access
	payload := ServiceUpdateRequest{Name: "New App", InternalURL: "http://192.168.9.25:5001/path?hostname=192.168.9.25", SameHost: true, ExternalURL: "https://app.example.test/", Health: ServiceHealthUpdateRequest{Type: "tcp", Address: "health.example.test:5001", Timeout: "2s"}}
	body, _ := json.Marshal(payload)
	rec := uiRequest(t, srv, http.MethodPost, "http://10.99.1.2/api/services", string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("create failed: %d %s", rec.Code, rec.Body.String())
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	created := cfg.Groups[0].Services[len(cfg.Groups[0].Services)-1]
	if created.InternalURL != "http://home-lan.example.test:5001/path?hostname=home-lan.example.test" || created.Health.Address != payload.Health.Address {
		t.Fatal("save did not preserve the stable URL and independent health target")
	}
	for _, next := range []string{"http://192.168.50.8:5001/changed", "", "https://other-device.example.test/"} {
		payload.InternalURL = next
		payload.SameHost = next != "" && !strings.Contains(next, "other-device")
		body, _ = json.Marshal(payload)
		rec = uiRequest(t, srv, http.MethodPut, "http://100.122.1.8/api/services/"+created.ID, string(body))
		if rec.Code != http.StatusOK {
			t.Fatalf("edit failed: %d %s", rec.Code, rec.Body.String())
		}
		var response struct {
			Navigation navigationData `json:"navigation"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		item := response.Navigation.Groups[0].Services[len(response.Navigation.Groups[0].Services)-1]
		want, _ := access.canonicalInternalURL(next, payload.SameHost)
		if item.InternalURL != want || item.InternalAccessURL != access.internalURL(want, "100.122.1.8") || item.SameHost != payload.SameHost {
			t.Fatalf("source switch retained stale ownership or URL: %#v", item)
		}
		cfg, err = LoadConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(cfg.Access, access) || cfg.Groups[0].Services[len(cfg.Groups[0].Services)-1].Health.Address != payload.Health.Address {
			t.Fatal("entry editing changed access rules or health target")
		}
	}
	content, _ := os.ReadFile(path)
	if strings.Contains(string(content), "same_host") || strings.Contains(string(content), "internal_access_url") || strings.Contains(string(content), "100.122.1.8") {
		t.Fatal("editing intent or request address became a second persisted source")
	}
}
