package server

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
