package server

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
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
	srv, err := New("../../config.example.yaml")
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
	srv, err := New("../../config.example.yaml")
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
}

func TestIndexIncludesDragSortControls(t *testing.T) {
	srv, err := New("../../config.example.yaml")
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
	for _, want := range []string{"id=\"save-sort-button\"", "id=\"delete-service-button\"", "id=\"delete-confirm-backdrop\"", "id=\"confirm-delete-button\"", "data-action=\"delete\"", "/api/services/sort", "method: 'DELETE'", "openDeleteConfirm", "performDelete", "startDragPointer", "startMouseDrag", "dragPlaceholderFor", "animateGridMove", "layoutSortRect", "sortRowsFor", "sortAnimationToken", "sortSaving", "sortSaveQueued", "cubic-bezier(.22,1,.36,1)", "requestAnimationFrame", "sortPayload"} {
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
	srv, err := New("../../config.example.yaml")
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
	for _, want := range []string{"id=\"access-mode-button\"", "home-nav.access-mode", "mdi:web"} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected login to contain %q", want)
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

	body := []byte(`{"background_color":"#123abc","background_image":"/uploads/bg.webp"}`)
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
	if cfg.Appearance.BackgroundColor != "#123abc" || cfg.Appearance.BackgroundImage != "/uploads/bg.webp" {
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
	if !strings.HasPrefix(payload.URL, "/uploads/") || !strings.HasSuffix(payload.URL, ".svg") {
		t.Fatalf("unexpected upload url: %q", payload.URL)
	}

	assetReq := httptest.NewRequest(http.MethodGet, payload.URL, nil)
	assetRec := httptest.NewRecorder()
	srv.ServeHTTP(assetRec, assetReq)
	if assetRec.Code != http.StatusOK {
		t.Fatalf("expected uploaded asset status %d, got %d", http.StatusOK, assetRec.Code)
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
