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

func testAccess() AccessConfig {
	return AccessConfig{HostAliases: map[string][]string{
		"192.168.1.10": {"10.20.0.10", "100.80.0.10", "fd7a:115c:a1e0::10"},
	}}
}

func TestAccessURLFollowsKnownHostOnly(t *testing.T) {
	cases := []struct{ name, host, input, want string }{
		{"netmaker", "10.20.0.10:3002", "http://192.168.1.10:5001", "http://10.20.0.10:5001"},
		{"tailscale", "100.80.0.10:3002", "https://192.168.1.10/ui/?a=1&b=%2F#login", "https://100.80.0.10/ui/?a=1&b=%2F#login"},
		{"controller", "10.20.0.10", "http://192.168.1.10:9091/?hostname=192.168.1.10&port=9097&note=192.168.1.10", "http://10.20.0.10:9091/?hostname=10.20.0.10&port=9097&note=192.168.1.10"},
		{"other controller", "10.20.0.10", "http://192.168.1.10:9091/?hostname=192.168.1.20", "http://10.20.0.10:9091/?hostname=192.168.1.20"},
		{"other device", "10.20.0.10:3002", "https://192.168.1.20/ui/#login", "https://192.168.1.20/ui/#login"},
		{"internal domain", "100.80.0.10", "https://app-lan.example.test/ui/", "https://app-lan.example.test/ui/"},
		{"public domain", "home.example.test", "http://192.168.1.10:5001", "http://192.168.1.10:5001"},
		{"lan domain", "home-lan.example.test", "http://192.168.1.10:5001", "http://192.168.1.10:5001"},
		{"lan address", "192.168.1.10:3002", "http://192.168.1.10:5001", "http://192.168.1.10:5001"},
		{"unknown 10 address", "10.20.0.11:3002", "http://192.168.1.10:5001", "http://192.168.1.10:5001"},
		{"unknown 100 address", "100.80.0.11:3002", "http://192.168.1.10:5001", "http://192.168.1.10:5001"},
		{"lookalike domain", "10.20.0.10.example.test", "http://192.168.1.10:5001", "http://192.168.1.10:5001"},
		{"empty", "10.20.0.10", "", ""},
		{"ipv6 port", "[fd7a:115c:a1e0::10]:3002", "http://192.168.1.10:5001/", "http://[fd7a:115c:a1e0::10]:5001/"},
		{"ipv6 no port", "[fd7a:115c:a1e0::10]", "https://192.168.1.10/", "https://[fd7a:115c:a1e0::10]/"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := testAccess().internalURL(tt.input, tt.host); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAccessConfigValidationAndRoundTrip(t *testing.T) {
	for _, aliases := range []map[string][]string{
		{"192.168.1.10": {"10.20.0.10:3002"}},
		{"192.168.1.10": {"10.example.test"}},
		{"192.168.1.10": {"10.20.0.999"}},
		{"192.168.1.10": {"0.0.0.0"}},
		{"192.168.1.10": {"192.168.1.10"}},
		{"192.168.1.10": {"10.20.0.10"}, "10.20.0.10": {"100.80.0.10"}},
		{"192.168.1.10": {"10.20.0.10"}, "192.168.1.20": {"10.20.0.10"}},
	} {
		if err := (AccessConfig{HostAliases: aliases}).validate(); err == nil {
			t.Fatalf("ambiguous or invalid aliases accepted: %v", aliases)
		}
	}
	path := writeTempConfig(t, authTestConfig())
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Access = testAccess()
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.Access, reloaded.Access) {
		t.Fatal("save lost host aliases")
	}
	copy := cloneConfig(cfg)
	copy.Access.HostAliases["192.168.1.10"][0] = "10.20.0.11"
	if !reflect.DeepEqual(cfg.Access, testAccess()) {
		t.Fatal("clone mutated live aliases")
	}
}

func TestAliasPortsPreserveRestrictedServiceBoundaries(t *testing.T) {
	access := testAccess()
	access.AliasPorts = map[string][]int{"10.20.0.10": {80, 5001}, "100.80.0.10": {}}
	for _, tt := range []struct{ host, input, want string }{
		{"10.20.0.10", "http://192.168.1.10:5001/", "http://10.20.0.10:5001/"},
		{"10.20.0.10", "http://192.168.1.10/", "http://10.20.0.10/"},
		{"10.20.0.10", "https://192.168.1.10/", ""},
		{"10.20.0.10", "http://192.168.1.10:8090/", ""},
		{"100.80.0.10", "http://192.168.1.10:5001/", ""},
		{"[fd7a:115c:a1e0::10]", "http://192.168.1.10:8090/", "http://[fd7a:115c:a1e0::10]:8090/"},
		{"192.168.1.10", "http://192.168.1.10:8090/", "http://192.168.1.10:8090/"},
		{"10.20.0.10", "http://192.168.1.20:8090/", "http://192.168.1.20:8090/"},
	} {
		if got := access.internalURL(tt.input, tt.host); got != tt.want {
			t.Fatalf("host %s: got %q, want %q", tt.host, got, tt.want)
		}
	}
	path := writeTempConfig(t, authTestConfig())
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Access = access
	cfg.Groups[0].Services[0].InternalURL = "http://192.168.1.10:8090/"
	cfg.Groups[0].Services[0].ExternalURL = "https://app.example.test/"
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reloaded.Access, access) {
		t.Fatal("port restrictions lost on save")
	}
	copy := cloneConfig(cfg)
	copy.Access.AliasPorts["10.20.0.10"][0] = 443
	if cfg.Access.AliasPorts["10.20.0.10"][0] != 80 {
		t.Fatal("clone altered live ports")
	}
	data := navigationState(reloaded, "10.20.0.10:3002")
	item := data.Groups[0].Services[0]
	if item.InternalAccessURL != "" || item.InternalURL == "" || item.ExternalURL == "" {
		t.Fatal("blocked direct URL must retain source and external fallback")
	}
	for _, ports := range []map[string][]int{{"10.20.0.11": {80}}, {"10.20.0.10": {0}}, {"10.20.0.10": {65536}}, {"10.20.0.10": {80, 80}}} {
		invalid := testAccess()
		invalid.AliasPorts = ports
		if err := invalid.validate(); err == nil {
			t.Fatalf("invalid ports accepted: %v", ports)
		}
	}
}

func TestVPNNavigationKeepsConfiguredURLsAcrossEdits(t *testing.T) {
	path := writeTempConfig(t, authTestConfig())
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Access = testAccess()
	cfg.Groups[0].Services[0].InternalURL = "http://192.168.1.10:5001/path?q=1#view"
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	srv, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"10.20.0.10", "100.80.0.10"} {
		base := "http://" + host + ":3002"
		login := httptest.NewRecorder()
		srv.ServeHTTP(login, httptest.NewRequest(http.MethodGet, base+"/login", nil))
		if !strings.Contains(login.Body.String(), `const defaultAccessMode = "internal";`) {
			t.Fatal("login must use VPN default")
		}
		for _, endpoint := range []string{"/", "/api/navigation"} {
			rec := uiRequest(t, srv, http.MethodGet, base+endpoint, "")
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"internal_access_url":"http://`+host+`:5001/path?q=1#view"`) {
				t.Fatalf("VPN navigation missing on %s: %d", endpoint, rec.Code)
			}
		}
	}
	// An edit must refresh the derived link without replacing the YAML source.
	for _, raw := range []string{"http://192.168.1.10:5010/changed", "http://192.168.1.20:5010/other", ""} {
		body, _ := json.Marshal(ServiceUpdateRequest{Name: "App", InternalURL: raw, ExternalURL: "https://app.example.test", Health: ServiceHealthUpdateRequest{Type: "disabled"}})
		rec := uiRequest(t, srv, http.MethodPut, "http://10.20.0.10:3002/api/services/app", string(body))
		if rec.Code != http.StatusOK {
			t.Fatalf("edit failed: %d %s", rec.Code, rec.Body.String())
		}
		var result struct {
			Navigation navigationData `json:"navigation"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		item := result.Navigation.Groups[0].Services[0]
		if item.InternalURL != raw || item.InternalAccessURL != cfg.Access.internalURL(raw, "10.20.0.10") {
			t.Fatalf("stale or overwritten address: %+v", item)
		}
		reloaded, err := LoadConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		if reloaded.Groups[0].Services[0].InternalURL != raw || !reflect.DeepEqual(reloaded.Access, cfg.Access) {
			t.Fatal("edit altered source addresses or mappings")
		}
	}
	content, _ := os.ReadFile(path)
	if strings.Contains(string(content), "internal_access_url") {
		t.Fatal("derived address was persisted")
	}
	request := httptest.NewRequest(http.MethodGet, "http://home.example.test/api/navigation", nil)
	request.Header.Set("X-Forwarded-Host", "10.20.0.10:3002")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: srv.newSession("admin")})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, request)
	var data navigationData
	if err := json.Unmarshal(rec.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if data.DefaultAccessMode != "external" {
		t.Fatal("forwarded host must not select VPN mapping")
	}
}
