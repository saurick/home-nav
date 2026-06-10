package server

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHealthz(t *testing.T) {
	srv, err := New("../../config.example.yaml")
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	if rec.Body.String() != "ok\n" {
		t.Fatalf("unexpected body: %q", rec.Body.String())
	}
}

func TestFavicon(t *testing.T) {
	srv, err := New("../../config.example.yaml")
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/favicon.svg", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/svg+xml; charset=utf-8" {
		t.Fatalf("unexpected content type: %q", got)
	}
	if !strings.Contains(rec.Body.String(), "<svg") {
		t.Fatalf("expected svg body, got %q", rec.Body.String())
	}
}

func TestStatusEndpointReturnsCachedStatus(t *testing.T) {
	srv, err := New(writeTempConfig(t, publicExampleConfig(t)))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var payload StatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if len(payload.Services) != 3 {
		t.Fatalf("expected 3 services, got %d", len(payload.Services))
	}
	if payload.Services["drawio"].Status != StatusDisabled {
		t.Fatalf("expected disabled drawio, got %#v", payload.Services["drawio"])
	}
}

func TestIndexIncludesAccessModeToggle(t *testing.T) {
	srv, err := New(writeTempConfig(t, publicExampleConfig(t)))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"id=\"access-mode-button\"", "data-internal-url=", "data-external-url=", "home-nav.access-mode"} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected index to contain %q", want)
		}
	}
	if !strings.Contains(body, `href="https://dockge.example.com"`) {
		t.Fatal("index should render service clicks as direct links")
	}
	for _, want := range []string{
		`rel="noopener noreferrer"`,
		"function openRedirectHref(url)",
		"window.open(openRedirectHref(url), '_blank', 'noopener,noreferrer')",
		"openEntryURL(preferredURL(item, accessMode))",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected index to contain %q", want)
		}
	}
	if !strings.Contains(body, "function copyTextFallback(value)") || !strings.Contains(body, "document.execCommand('copy')") || !strings.Contains(body, "window.isSecureContext") {
		t.Fatal("index should provide an insecure-http copy fallback for server access")
	}
	if !strings.Contains(body, "function showManualCopy(value)") || !strings.Contains(body, "window.prompt('请手动复制链接', value)") {
		t.Fatal("index should show the link when browser copy APIs are unavailable")
	}
}

func TestIndexDoesNotRenderVisibleTitle(t *testing.T) {
	srv, err := New(writeTempConfig(t, publicExampleConfig(t)))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<title>Home</title>") {
		t.Fatal("index should keep the configured browser title")
	}
	if strings.Contains(body, "<h1>Home</h1>") {
		t.Fatal("index should not render the configured title as visible heading")
	}
}

func TestOpenRedirectPage(t *testing.T) {
	srv, err := New(writeTempConfig(t, publicExampleConfig(t)))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	target := "https://www.youtube.com/watch?v=abc123"
	req := httptest.NewRequest(http.MethodGet, "/open?url="+url.QueryEscape(target), nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("unexpected referrer policy: %q", got)
	}
	body := rec.Body.String()
	for _, want := range []string{"<title>Opening...</title>", "window.location.replace(target)", target} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected open redirect page to contain %q", want)
		}
	}
}

func TestOpenRedirectRejectsInvalidURL(t *testing.T) {
	srv, err := New(writeTempConfig(t, publicExampleConfig(t)))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/open?url="+url.QueryEscape("javascript:alert(1)"), nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}
}

func TestIndexDoesNotRenderClockOrSearch(t *testing.T) {
	srv, err := New(writeTempConfig(t, publicExampleConfig(t)))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	body := rec.Body.String()
	for _, unwanted := range []string{"id=\"search\"", "class=\"search-wrap\"", "clock-time", "clock-date", "搜索服务、描述或标签", "updateClock", "home-nav.search"} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("index should not contain clock or search UI marker %q", unwanted)
		}
	}
}

func TestIndexIncludesDragSortControls(t *testing.T) {
	srv, err := New(writeTempConfig(t, publicExampleConfig(t)))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"id=\"save-sort-button\"", "id=\"delete-service-button\"", "id=\"delete-confirm-backdrop\"", "id=\"confirm-delete-button\"", "data-action=\"delete\"", "/api/services/sort", "method: 'DELETE'", "openDeleteConfirm", "performDelete", "startDragPointer", "startMouseDrag", "dragPlaceholderFor", "animateGridMove", "layoutSortRect", "sortRowsFor", "sortAnimations", "node.animate", "cubic-bezier(.16,1,.3,1)", "requestAnimationFrame", "sortPayload"} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected index to contain %q", want)
		}
	}
	if !strings.Contains(body, ".drag-placeholder { width: 100%; min-height: 122px; visibility: hidden; pointer-events: none; }") {
		t.Fatal("drag placeholder should reserve layout space without drawing a visible drop box")
	}
	if strings.Contains(body, "window.confirm") {
		t.Fatal("index should use the in-page delete confirmation modal instead of window.confirm")
	}
}

func TestIndexIncludesGroupManagementControls(t *testing.T) {
	srv, err := New(writeTempConfig(t, publicExampleConfig(t)))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"id=\"open-groups-button\"", "id=\"groups-backdrop\"", "id=\"group-form\"", "id=\"group-list\"", "id=\"save-group-sort-button\"", "data-action=\"manage-groups\"", "data-action=\"edit-group\"", "data-action=\"delete-group\"", "/api/groups/sort", "/api/groups/"} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected index to contain %q", want)
		}
	}
}

func TestIndexIncludesGalleryControls(t *testing.T) {
	srv, err := New(writeTempConfig(t, publicExampleConfig(t)))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"id=\"open-gallery-button\"", "id=\"gallery-backdrop\"", "id=\"gallery-grid\"", "data-gallery-filter=\"wallpaper\"", "data-gallery-filter=\"icon\"", "data-upload-type=\"icon\"", "data-upload-type=\"wallpaper\"", "formData.append('asset_type', assetType)", "formData.append('asset_type', 'icon')", "formData.append('asset_type', 'wallpaper')", "/api/assets", "delete-asset", "use-background-asset", "use-icon-asset", "galleryMode === 'background'"} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected index to contain %q", want)
		}
	}
}

func TestIndexIncludesAdaptiveBackgroundControls(t *testing.T) {
	srv, err := New(writeTempConfig(t, publicExampleConfig(t)))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"data-background-overlay=\"medium\"", "name=\"background_overlay\"", "backgroundOverlayAlpha", "var(--control-bg)", "backdrop-filter: blur(14px)", "class=\"inline-icon"} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected index to contain %q", want)
		}
	}
	if strings.Contains(body, "code.iconify.design") || strings.Contains(body, "<iconify-icon") {
		t.Fatal("index should not depend on the Iconify web component runtime")
	}
}

func TestAuthProtectsIndexAndStatus(t *testing.T) {
	srv, err := New(writeTempConfig(t, authTestConfig()))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	indexReq := httptest.NewRequest(http.MethodGet, "/", nil)
	indexRec := httptest.NewRecorder()
	srv.ServeHTTP(indexRec, indexReq)
	if indexRec.Code != http.StatusSeeOther {
		t.Fatalf("expected index redirect, got %d", indexRec.Code)
	}
	if got := indexRec.Header().Get("Location"); got != "/login" {
		t.Fatalf("unexpected redirect: %q", got)
	}

	statusReq := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	statusRec := httptest.NewRecorder()
	srv.ServeHTTP(statusRec, statusReq)
	if statusRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized status, got %d", statusRec.Code)
	}
}

func TestExampleConfigRedirectsToSetup(t *testing.T) {
	srv, err := New(writeTempConfig(t, exampleConfig(t)))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	indexReq := httptest.NewRequest(http.MethodGet, "/", nil)
	indexRec := httptest.NewRecorder()
	srv.ServeHTTP(indexRec, indexReq)
	if indexRec.Code != http.StatusSeeOther {
		t.Fatalf("expected index redirect, got %d", indexRec.Code)
	}
	if got := indexRec.Header().Get("Location"); got != "/setup" {
		t.Fatalf("unexpected redirect: %q", got)
	}

	statusReq := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	statusRec := httptest.NewRecorder()
	srv.ServeHTTP(statusRec, statusReq)
	if statusRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected setup to protect api status, got %d", statusRec.Code)
	}
}

func TestSetupPageAllowsPrivateSource(t *testing.T) {
	srv, err := New(writeTempConfig(t, exampleConfig(t)))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/setup", nil)
	req.RemoteAddr = "192.168.1.23:45678"
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"首次使用，请设置管理员账号。",
		"id=\"setup-password\"",
		"id=\"setup-confirm-password\"",
		"data-target=\"setup-password\"",
		"data-target=\"setup-confirm-password\"",
		"data-password-toggle",
		"完成设置",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected setup page to contain %q", want)
		}
	}
}

func TestSetupPageBlocksPublicSource(t *testing.T) {
	srv, err := New(writeTempConfig(t, exampleConfig(t)))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/setup", nil)
	req.RemoteAddr = "203.0.113.10:45678"
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "当前访问来源不是局域网或本机") {
		t.Fatalf("expected setup block message, got %q", body)
	}
	if strings.Contains(body, "name=\"password\"") {
		t.Fatal("public setup page should not render password form")
	}
}

func TestSetupPageBlocksPublicForwardedSource(t *testing.T) {
	srv, err := New(writeTempConfig(t, exampleConfig(t)))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/setup", nil)
	req.RemoteAddr = "127.0.0.1:45678"
	req.Header.Set("X-Forwarded-For", "203.0.113.10")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "当前访问来源不是局域网或本机") {
		t.Fatalf("expected setup block message, got %q", body)
	}
	if strings.Contains(body, "name=\"password\"") {
		t.Fatal("public forwarded setup page should not render password form")
	}
}

func TestSetupInitializesAuth(t *testing.T) {
	configPath := writeTempConfig(t, exampleConfig(t))
	srv, err := New(configPath)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	form := url.Values{}
	form.Set("username", "owner")
	form.Set("password", "new-password")
	form.Set("confirm_password", "new-password")
	req := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "10.0.0.8:45678"
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected setup redirect, got %d", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/" {
		t.Fatalf("unexpected redirect: %q", got)
	}
	if len(rec.Result().Cookies()) == 0 {
		t.Fatal("setup should create a login session")
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("saved config did not reload: %v", err)
	}
	if !cfg.Auth.Enabled || cfg.Auth.Username != "owner" || cfg.Auth.Password != "new-password" {
		t.Fatalf("auth was not initialized: %#v", cfg.Auth)
	}
	if cfg.Auth.SessionSecret == defaultAuthSessionSecret || len(cfg.Auth.SessionSecret) < 32 {
		t.Fatalf("session secret was not regenerated: %q", cfg.Auth.SessionSecret)
	}
}

func TestLoginIncludesAccessModeToggle(t *testing.T) {
	srv, err := New(writeTempConfig(t, authTestConfig()))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"id=\"access-mode-button\"",
		"home-nav.access-mode",
		"id=\"login-password\"",
		"data-target=\"login-password\"",
		"data-password-toggle",
		"aria-label=\"显示密码\"",
		"class=\"inline-icon",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected login to contain %q", want)
		}
	}
	if strings.Contains(body, "code.iconify.design") || strings.Contains(body, "<iconify-icon") {
		t.Fatal("login should not depend on the Iconify web component runtime")
	}
}

func TestLoginDoesNotRenderVisibleTitle(t *testing.T) {
	srv, err := New(writeTempConfig(t, authTestConfig()))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<title>登录 - 测试导航</title>") {
		t.Fatal("login should keep the configured browser title")
	}
	for _, unwanted := range []string{"<h1>测试导航</h1>", "请登录后查看。"} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("login should not contain visible title text %q", unwanted)
		}
	}
}

func TestAuthLoginFlow(t *testing.T) {
	srv, err := New(writeTempConfig(t, authTestConfig()))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	testServer := httptest.NewServer(srv)
	defer testServer.Close()

	client := testServer.Client()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	client.Jar = jar

	badResp, err := client.PostForm(testServer.URL+"/login", map[string][]string{
		"username": {"admin"},
		"password": {"bad-password"},
	})
	if err != nil {
		t.Fatalf("bad login request failed: %v", err)
	}
	_ = badResp.Body.Close()
	if badResp.StatusCode != http.StatusOK {
		t.Fatalf("expected bad login page, got %d", badResp.StatusCode)
	}

	goodResp, err := client.PostForm(testServer.URL+"/login?return_to=/api/status", map[string][]string{
		"username": {"admin"},
		"password": {"test-password"},
	})
	if err != nil {
		t.Fatalf("good login request failed: %v", err)
	}
	defer goodResp.Body.Close()
	if goodResp.StatusCode != http.StatusOK {
		t.Fatalf("expected redirected api status, got %d", goodResp.StatusCode)
	}
	var payload StatusResponse
	if err := json.NewDecoder(goodResp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode status payload: %v", err)
	}
	if len(payload.Services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(payload.Services))
	}
}

func TestServiceUpdateSavesConfig(t *testing.T) {
	configPath := writeTempConfig(t, authTestConfig())
	srv, err := New(configPath)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	body := []byte(`{
		"name": "Renamed App",
		"description": "更新后的入口",
		"icon_text": "RA",
		"icon": "mdi:application",
		"internal_url": "http://app.example.local/new",
		"external_url": "https://app.example.test",
		"tags": ["ops", "tools"],
		"notes": "保存测试",
		"group_id": "ops",
		"health": {
			"type": "http",
			"url": "http://app.example.local/healthz",
			"expect_status": 204,
			"timeout": "1500ms"
		}
	}`)
	req := httptest.NewRequest(http.MethodPut, "/api/services/app", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: srv.newSession("admin", time.Now())})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("saved config did not reload: %v", err)
	}
	got := cfg.Groups[0].Services[0]
	if got.Name != "Renamed App" || got.Icon != "mdi:application" {
		t.Fatalf("service was not saved: %#v", got)
	}
	if got.Health.Type != "http" || got.Health.ExpectStatus != 204 || got.Health.Timeout != 1500*time.Millisecond {
		t.Fatalf("health was not saved: %#v", got.Health)
	}
}

func TestServiceCreateSavesConfig(t *testing.T) {
	configPath := writeTempConfig(t, authTestConfig())
	srv, err := New(configPath)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	body := []byte(`{
		"name": "New Tool",
		"description": "新增入口",
		"icon_text": "NT",
		"icon": "mdi:plus",
		"internal_url": "http://new.example.local",
		"external_url": "",
		"tags": ["tools"],
		"notes": "新增测试",
		"group_id": "ops",
		"health": {
			"type": "disabled",
			"timeout": "2s"
		}
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/services", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: srv.newSession("admin", time.Now())})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("saved config did not reload: %v", err)
	}
	if len(cfg.Groups[0].Services) != 2 {
		t.Fatalf("expected 2 services, got %d", len(cfg.Groups[0].Services))
	}
	got := cfg.Groups[0].Services[1]
	if got.ID != "new-tool" || got.Name != "New Tool" || got.Health.Type != "disabled" {
		t.Fatalf("service was not created correctly: %#v", got)
	}
}

func TestServiceDeleteSavesConfig(t *testing.T) {
	configPath := writeTempConfig(t, authTestConfig())
	srv, err := New(configPath)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/services/app", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: srv.newSession("admin", time.Now())})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("saved config did not reload: %v", err)
	}
	if len(cfg.Groups) != 1 {
		t.Fatalf("expected group to remain, got %d groups", len(cfg.Groups))
	}
	if len(cfg.Groups[0].Services) != 0 {
		t.Fatalf("expected service to be deleted, got %#v", cfg.Groups[0].Services)
	}

	statusReq := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	statusReq.AddCookie(&http.Cookie{Name: sessionCookieName, Value: srv.newSession("admin", time.Now())})
	statusRec := httptest.NewRecorder()
	srv.ServeHTTP(statusRec, statusReq)
	if statusRec.Code != http.StatusOK {
		t.Fatalf("expected status endpoint %d, got %d", http.StatusOK, statusRec.Code)
	}
	var payload StatusResponse
	if err := json.Unmarshal(statusRec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid status json: %v", err)
	}
	if len(payload.Services) != 0 {
		t.Fatalf("expected no service statuses, got %#v", payload.Services)
	}
}

func TestServiceSortSavesConfig(t *testing.T) {
	configPath := writeTempConfig(t, sortTestConfig())
	srv, err := New(configPath)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	body := []byte(`{
		"groups": [
			{"group_id": "ops", "service_ids": ["metrics"]},
			{"group_id": "tools", "service_ids": ["app", "drawio"]}
		]
	}`)
	req := httptest.NewRequest(http.MethodPut, "/api/services/sort", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: srv.newSession("admin", time.Now())})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("saved config did not reload: %v", err)
	}
	gotOps := serviceIDs(cfg.Groups[0].Services)
	gotTools := serviceIDs(cfg.Groups[1].Services)
	if strings.Join(gotOps, ",") != "metrics" {
		t.Fatalf("unexpected ops order: %v", gotOps)
	}
	if strings.Join(gotTools, ",") != "app,drawio" {
		t.Fatalf("unexpected tools order: %v", gotTools)
	}
}

func TestServiceSortRejectsIncompleteOrder(t *testing.T) {
	configPath := writeTempConfig(t, sortTestConfig())
	srv, err := New(configPath)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	body := []byte(`{
		"groups": [
			{"group_id": "ops", "service_ids": ["app"]}
		]
	}`)
	req := httptest.NewRequest(http.MethodPut, "/api/services/sort", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: srv.newSession("admin", time.Now())})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d: %s", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

func TestGroupCreateUpdateSortAndDeleteSavesConfig(t *testing.T) {
	configPath := writeTempConfig(t, sortTestConfig())
	srv, err := New(configPath)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	cookie := &http.Cookie{Name: sessionCookieName, Value: srv.newSession("admin", time.Now())}

	createReq := httptest.NewRequest(http.MethodPost, "/api/groups", bytes.NewReader([]byte(`{"name":"媒体服务"}`)))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.AddCookie(cookie)
	createRec := httptest.NewRecorder()
	srv.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusOK {
		t.Fatalf("expected create status %d, got %d: %s", http.StatusOK, createRec.Code, createRec.Body.String())
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("saved config did not reload after create: %v", err)
	}
	if len(cfg.Groups) != 3 || cfg.Groups[2].Name != "媒体服务" {
		t.Fatalf("group was not created correctly: %#v", cfg.Groups)
	}
	createdID := cfg.Groups[2].ID

	updateReq := httptest.NewRequest(http.MethodPut, "/api/groups/"+createdID, bytes.NewReader([]byte(`{"name":"影音服务"}`)))
	updateReq.Header.Set("Content-Type", "application/json")
	updateReq.AddCookie(cookie)
	updateRec := httptest.NewRecorder()
	srv.ServeHTTP(updateRec, updateReq)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("expected update status %d, got %d: %s", http.StatusOK, updateRec.Code, updateRec.Body.String())
	}

	sortReq := httptest.NewRequest(http.MethodPut, "/api/groups/sort", bytes.NewReader([]byte(`{"group_ids":["`+createdID+`","tools","ops"]}`)))
	sortReq.Header.Set("Content-Type", "application/json")
	sortReq.AddCookie(cookie)
	sortRec := httptest.NewRecorder()
	srv.ServeHTTP(sortRec, sortReq)
	if sortRec.Code != http.StatusOK {
		t.Fatalf("expected sort status %d, got %d: %s", http.StatusOK, sortRec.Code, sortRec.Body.String())
	}

	cfg, err = LoadConfig(configPath)
	if err != nil {
		t.Fatalf("saved config did not reload after sort: %v", err)
	}
	gotOrder := []string{cfg.Groups[0].ID, cfg.Groups[1].ID, cfg.Groups[2].ID}
	if strings.Join(gotOrder, ",") != createdID+",tools,ops" {
		t.Fatalf("unexpected group order: %v", gotOrder)
	}
	if cfg.Groups[0].Name != "影音服务" {
		t.Fatalf("group name was not updated: %#v", cfg.Groups[0])
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/groups/"+createdID, nil)
	deleteReq.AddCookie(cookie)
	deleteRec := httptest.NewRecorder()
	srv.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("expected delete status %d, got %d: %s", http.StatusOK, deleteRec.Code, deleteRec.Body.String())
	}

	cfg, err = LoadConfig(configPath)
	if err != nil {
		t.Fatalf("saved config did not reload after delete: %v", err)
	}
	gotOrder = []string{cfg.Groups[0].ID, cfg.Groups[1].ID}
	if strings.Join(gotOrder, ",") != "tools,ops" {
		t.Fatalf("unexpected group order after delete: %v", gotOrder)
	}
}

func TestGroupDeleteRejectsNonEmptyGroup(t *testing.T) {
	configPath := writeTempConfig(t, sortTestConfig())
	srv, err := New(configPath)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/groups/ops", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: srv.newSession("admin", time.Now())})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d: %s", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "分组下还有入口") {
		t.Fatalf("expected non-empty group error, got %s", rec.Body.String())
	}
}

func TestSettingsUpdateSavesAppearance(t *testing.T) {
	configPath := writeTempConfig(t, authTestConfig())
	srv, err := New(configPath)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	body := []byte(`{"background_color":"#123abc","background_image":"/uploads/bg.webp","background_overlay":"high"}`)
	req := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: srv.newSession("admin", time.Now())})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("saved config did not reload: %v", err)
	}
	if cfg.Appearance.BackgroundColor != "#123abc" || cfg.Appearance.BackgroundImage != "/uploads/bg.webp" || cfg.Appearance.BackgroundOverlay != "high" {
		t.Fatalf("appearance was not saved: %#v", cfg.Appearance)
	}
}

func TestServesConfiguredUploadAssets(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "icon.svg"), []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`), 0600); err != nil {
		t.Fatalf("write icon: %v", err)
	}
	srv, err := New(writeTempConfig(t, `
title: 测试导航
check_interval: 30s
assets:
  uploads_dir: `+dir+`
  uploads_url_prefix: /uploads/
groups:
  - id: ops
    name: 运维
    services:
      - id: app
        name: App
        icon: /uploads/icon.svg
        internal_url: http://app.example.local
        health:
          type: disabled
`))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/uploads/icon.svg", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	if rec.Body.Len() == 0 {
		t.Fatal("expected icon body")
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, max-age=604800" {
		t.Fatalf("unexpected cache control: %q", got)
	}
}

func TestUploadIcon(t *testing.T) {
	dir := t.TempDir()
	srv, err := New(writeTempConfig(t, `
title: 测试导航
check_interval: 30s
auth:
  enabled: true
  username: admin
  password: test-password
  session_secret: 0123456789abcdef0123456789abcdef
  session_ttl: 24h
assets:
  uploads_dir: `+dir+`
  uploads_url_prefix: /uploads/
groups:
  - id: ops
    name: 运维
    services:
      - id: app
        name: App
        internal_url: http://app.example.local
        health:
          type: disabled
`))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "icon.svg")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`)); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := writer.WriteField("asset_type", "icon"); err != nil {
		t.Fatalf("write asset type: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/uploads", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: srv.newSession("admin", time.Now())})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var payload struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	if !strings.HasPrefix(payload.URL, "/uploads/icons/") || !strings.HasSuffix(payload.URL, ".svg") {
		t.Fatalf("unexpected upload url: %q", payload.URL)
	}

	assetReq := httptest.NewRequest(http.MethodGet, payload.URL, nil)
	assetRec := httptest.NewRecorder()
	srv.ServeHTTP(assetRec, assetReq)
	if assetRec.Code != http.StatusOK {
		t.Fatalf("expected uploaded asset status %d, got %d", http.StatusOK, assetRec.Code)
	}
}

func TestTypedUploadClassificationOverridesImageShape(t *testing.T) {
	dir := t.TempDir()
	srv, err := New(writeTempConfig(t, `
title: 测试导航
check_interval: 30s
auth:
  enabled: true
  username: admin
  password: test-password
  session_secret: 0123456789abcdef0123456789abcdef
  session_ttl: 24h
assets:
  uploads_dir: `+dir+`
  uploads_url_prefix: /uploads/
groups:
  - id: ops
    name: 运维
    services:
      - id: app
        name: App
        internal_url: http://app.example.local
        health:
          type: disabled
`))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	cookie := &http.Cookie{Name: sessionCookieName, Value: srv.newSession("admin", time.Now())}

	iconURL := uploadTestPNG(t, srv, cookie, "wide-icon.png", 1000, 500, "icon")
	wallpaperURL := uploadTestPNG(t, srv, cookie, "square-wallpaper.png", 400, 400, "wallpaper")
	if !strings.HasPrefix(iconURL, "/uploads/icons/") {
		t.Fatalf("expected icon upload directory, got %q", iconURL)
	}
	if !strings.HasPrefix(wallpaperURL, "/uploads/wallpapers/") {
		t.Fatalf("expected wallpaper upload directory, got %q", wallpaperURL)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/assets", nil)
	listReq.AddCookie(cookie)
	listRec := httptest.NewRecorder()
	srv.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("expected list status %d, got %d: %s", http.StatusOK, listRec.Code, listRec.Body.String())
	}
	var listPayload struct {
		Assets []AssetItem `json:"assets"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listPayload); err != nil {
		t.Fatalf("decode asset list: %v", err)
	}
	byURL := make(map[string]AssetItem)
	for _, asset := range listPayload.Assets {
		byURL[asset.URL] = asset
	}
	if byURL[iconURL].Type != "icon" {
		t.Fatalf("wide icon upload should remain icon, got %#v", byURL[iconURL])
	}
	if byURL[wallpaperURL].Type != "wallpaper" {
		t.Fatalf("square wallpaper upload should remain wallpaper, got %#v", byURL[wallpaperURL])
	}
}

func TestAssetGalleryListsAndDeletesUnusedAssets(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "2026", "6", "9"), 0700); err != nil {
		t.Fatalf("mkdir upload tree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "icon.svg"), []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`), 0600); err != nil {
		t.Fatalf("write icon: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "unused.svg"), []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`), 0600); err != nil {
		t.Fatalf("write unused: %v", err)
	}
	writePNG(t, filepath.Join(dir, "2026", "6", "9", "wallpaper.png"), 1000, 500)

	srv, err := New(writeTempConfig(t, `
title: 测试导航
check_interval: 30s
auth:
  enabled: true
  username: admin
  password: test-password
  session_secret: 0123456789abcdef0123456789abcdef
  session_ttl: 24h
assets:
  uploads_dir: `+dir+`
  uploads_url_prefix: /uploads/
appearance:
  background_color: "#000000"
  background_image: /uploads/2026/6/9/wallpaper.png
groups:
  - id: ops
    name: 运维
    services:
      - id: app
        name: App
        icon: /uploads/icon.svg
        internal_url: http://app.example.local
        health:
          type: disabled
`))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	cookie := &http.Cookie{Name: sessionCookieName, Value: srv.newSession("admin", time.Now())}

	listReq := httptest.NewRequest(http.MethodGet, "/api/assets", nil)
	listReq.AddCookie(cookie)
	listRec := httptest.NewRecorder()
	srv.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("expected list status %d, got %d: %s", http.StatusOK, listRec.Code, listRec.Body.String())
	}
	var listPayload struct {
		Assets []AssetItem `json:"assets"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &listPayload); err != nil {
		t.Fatalf("decode asset list: %v", err)
	}
	if len(listPayload.Assets) != 3 {
		t.Fatalf("expected 3 assets, got %#v", listPayload.Assets)
	}
	byURL := make(map[string]AssetItem)
	for _, asset := range listPayload.Assets {
		byURL[asset.URL] = asset
	}
	if byURL["/uploads/icon.svg"].Type != "icon" || len(byURL["/uploads/icon.svg"].UsedBy) != 1 {
		t.Fatalf("expected used icon asset, got %#v", byURL["/uploads/icon.svg"])
	}
	if byURL["/uploads/2026/6/9/wallpaper.png"].Type != "wallpaper" || len(byURL["/uploads/2026/6/9/wallpaper.png"].UsedBy) != 1 {
		t.Fatalf("expected used wallpaper asset, got %#v", byURL["/uploads/2026/6/9/wallpaper.png"])
	}

	usedDeleteReq := httptest.NewRequest(http.MethodDelete, "/api/assets?url=/uploads/icon.svg", nil)
	usedDeleteReq.AddCookie(cookie)
	usedDeleteRec := httptest.NewRecorder()
	srv.ServeHTTP(usedDeleteRec, usedDeleteReq)
	if usedDeleteRec.Code != http.StatusConflict {
		t.Fatalf("expected used delete status %d, got %d: %s", http.StatusConflict, usedDeleteRec.Code, usedDeleteRec.Body.String())
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/assets?url=/uploads/unused.svg", nil)
	deleteReq.AddCookie(cookie)
	deleteRec := httptest.NewRecorder()
	srv.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("expected delete status %d, got %d: %s", http.StatusOK, deleteRec.Code, deleteRec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "unused.svg")); !os.IsNotExist(err) {
		t.Fatalf("expected unused asset to be deleted, stat err=%v", err)
	}
}

func TestAssetDeleteRejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	srv, err := New(writeTempConfig(t, `
title: 测试导航
check_interval: 30s
auth:
  enabled: true
  username: admin
  password: test-password
  session_secret: 0123456789abcdef0123456789abcdef
  session_ttl: 24h
assets:
  uploads_dir: `+dir+`
  uploads_url_prefix: /uploads/
groups:
  - id: ops
    name: 运维
    services:
      - id: app
        name: App
        internal_url: http://app.example.local
        health:
          type: disabled
`))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	req := httptest.NewRequest(http.MethodDelete, "/api/assets?url=/uploads/../services.yaml", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: srv.newSession("admin", time.Now())})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected traversal status %d, got %d: %s", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

func TestServesCachedIconifyIcon(t *testing.T) {
	cacheDir := t.TempDir()
	iconDir := filepath.Join(cacheDir, "mdi")
	if err := os.MkdirAll(iconDir, 0700); err != nil {
		t.Fatalf("mkdir icon dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(iconDir, "nas.svg"), []byte(`<svg xmlns="http://www.w3.org/2000/svg"><path fill="white"/></svg>`), 0600); err != nil {
		t.Fatalf("write icon cache: %v", err)
	}
	srv, err := New(writeTempConfig(t, `
title: 测试导航
check_interval: 30s
assets:
  icon_cache_dir: `+cacheDir+`
groups:
  - id: ops
    name: 运维
    services:
      - id: app
        name: App
        icon: mdi:nas
        internal_url: http://app.example.local
        health:
          type: disabled
`))
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/.iconify/mdi/nas.svg", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/svg+xml; charset=utf-8" {
		t.Fatalf("unexpected content type: %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, max-age=604800" {
		t.Fatalf("unexpected cache control: %q", got)
	}
	if rec.Body.Len() == 0 {
		t.Fatal("expected icon body")
	}
}

func TestSaveConfigRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "services.yaml")
	cfg := &Config{
		Title:         "测试导航",
		CheckInterval: 30 * time.Second,
		Groups: []Group{
			{
				ID:   "tools",
				Name: "工具",
				Services: []Service{
					{
						ID:          "tool",
						Name:        "Tool",
						Icon:        "mdi:tools",
						ExternalURL: "https://example.com/path?a=1#section",
						Tags:        nil,
						Health: HealthCheck{
							Type:    "disabled",
							Timeout: 2 * time.Second,
						},
					},
				},
			},
		},
	}
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	got := loaded.Groups[0].Services[0]
	if got.ExternalURL != "https://example.com/path?a=1#section" {
		t.Fatalf("unexpected external url: %q", got.ExternalURL)
	}
	if len(got.Tags) != 0 {
		t.Fatalf("expected empty tags, got %#v", got.Tags)
	}
}

func TestConfigRejectsDuplicateServiceID(t *testing.T) {
	path := writeTempConfig(t, `
groups:
  - id: ops
    name: 运维
    services:
      - id: same
        name: A
        internal_url: http://a.example.local
        health:
          type: disabled
      - id: same
        name: B
        internal_url: http://b.example.local
        health:
          type: disabled
`)

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected duplicate service id error")
	}
}

func TestHTTPHealthCheck(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	service := Service{
		ID:        "api",
		Name:      "API",
		GroupID:   "ops",
		GroupName: "运维",
		Health: HealthCheck{
			Type:         "http",
			URL:          target.URL,
			ExpectStatus: http.StatusNoContent,
			Timeout:      time.Second,
		},
	}

	status := checkService(t.Context(), target.Client(), service)
	if status.Status != StatusHealthy {
		t.Fatalf("expected healthy, got %#v", status)
	}
}

func TestTCPHealthCheck(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			_ = conn.Close()
		}
	}()

	service := Service{
		ID:        "ssh",
		Name:      "SSH",
		GroupID:   "ops",
		GroupName: "运维",
		Health: HealthCheck{
			Type:    "tcp",
			Address: listener.Addr().String(),
			Timeout: time.Second,
		},
	}

	status := checkService(t.Context(), http.DefaultClient, service)
	if status.Status != StatusHealthy {
		t.Fatalf("expected healthy, got %#v", status)
	}
}

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "services.yaml")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func exampleConfig(t *testing.T) string {
	t.Helper()
	content, err := os.ReadFile("../../config.example.yaml")
	if err != nil {
		t.Fatalf("read example config: %v", err)
	}
	return string(content)
}

func publicExampleConfig(t *testing.T) string {
	t.Helper()
	content := exampleConfig(t)
	content = strings.ReplaceAll(content, "password: change-me", "password: configured-public-mode")
	content = strings.ReplaceAll(content, "session_secret: change-this-to-at-least-32-random-characters", "session_secret: configured-public-mode-secret")
	return content
}

func writePNG(t *testing.T, path string, width, height int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 255), G: uint8(y % 255), B: 120, A: 255})
		}
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create png: %v", err)
	}
	defer file.Close()
	if err := png.Encode(file, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
}

func uploadTestPNG(t *testing.T, srv *Server, cookie *http.Cookie, filename string, width, height int, assetType string) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 255), G: uint8(y % 255), B: 120, A: 255})
		}
	}
	var pngBody bytes.Buffer
	if err := png.Encode(&pngBody, img); err != nil {
		t.Fatalf("encode png upload: %v", err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(pngBody.Bytes()); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := writer.WriteField("asset_type", assetType); err != nil {
		t.Fatalf("write asset type: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/uploads", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected upload status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var payload struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	if payload.URL == "" {
		t.Fatal("expected upload url")
	}
	return payload.URL
}

func authTestConfig() string {
	return strings.TrimSpace(`
title: 测试导航
check_interval: 30s
auth:
  enabled: true
  username: admin
  password: test-password
  session_secret: 0123456789abcdef0123456789abcdef
  session_ttl: 24h
groups:
  - id: ops
    name: 运维
    services:
      - id: app
        name: App
        internal_url: http://app.example.local
        health:
          type: disabled
`) + "\n"
}

func sortTestConfig() string {
	return strings.TrimSpace(`
title: 测试导航
check_interval: 30s
auth:
  enabled: true
  username: admin
  password: test-password
  session_secret: 0123456789abcdef0123456789abcdef
  session_ttl: 24h
groups:
  - id: ops
    name: 运维
    services:
      - id: app
        name: App
        internal_url: http://app.example.local
        health:
          type: disabled
      - id: metrics
        name: Metrics
        internal_url: http://metrics.example.local
        health:
          type: disabled
  - id: tools
    name: 工具
    services:
      - id: drawio
        name: Draw.io
        internal_url: http://drawio.example.local
        health:
          type: disabled
`) + "\n"
}

func serviceIDs(services []Service) []string {
	ids := make([]string, 0, len(services))
	for _, service := range services {
		ids = append(ids, service.ID)
	}
	return ids
}
