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

func dynamicAccess() AccessConfig {
	return AccessConfig{
		HomeHost: "home-lan.example.test", DefaultPorts: []int{80, 5001, 9091},
		NetworkPorts: map[string][]int{
			"100.64.0.0/10":   {80, 5001, 8090, 9091},
			"192.168.0.0/16":  {80, 443, 5001, 8090, 9091},
			"192.168.20.0/24": {},
		},
	}
}

func TestHomeHostFollowsChangingAddresses(t *testing.T) {
	a := dynamicAccess()
	raw := "http://home-lan.example.test:5001/path?keep=%2F&hostname=home-lan.example.test&port=9097#view"
	for _, host := range []string{"10.20.0.10", "10.244.8.9", "100.64.0.1", "100.127.255.254", "192.168.10.22", "172.25.1.9"} {
		got := a.internalURL(raw, host+":3002")
		want := "http://" + host + ":5001/path?keep=%2F&hostname=" + host + "&port=9097#view"
		if got != want || a.defaultMode(host+":3002") != "internal" {
			t.Fatalf("address rotation %s: got %q, want %q", host, got, want)
		}
	}
	for _, tt := range []struct{ host, raw, want string }{
		{"[fd7a:115c:a1e0::123]:3002", "http://home-lan.example.test:5001/", "http://[fd7a:115c:a1e0::123]:5001/"},
		{"[fd00:23::21]", "http://home-lan.example.test/", "http://[fd00:23::21]/"},
		{"HOME-LAN.EXAMPLE.TEST.:3002", raw, raw},
		{"100.90.0.23", "http://home-lan.example.test:8090/_/", "http://100.90.0.23:8090/_/"},
		{"10.32.0.23", "http://home-lan.example.test:8090/", ""},
		{"192.168.10.5", "https://home-lan.example.test/", "https://192.168.10.5/"},
		{"192.168.20.5", "http://home-lan.example.test:5001/", ""},
		{"10.32.0.23", "http://192.168.1.55:5001/", "http://192.168.1.55:5001/"},
		{"10.32.0.23", "https://device-lan.example.test/", "https://device-lan.example.test/"},
		{"10.32.0.23", "http://home-lan.example.test:5001/?hostname=device-lan.example.test&note=home-lan.example.test", "http://10.32.0.23:5001/?hostname=device-lan.example.test&note=home-lan.example.test"},
		{"10.32.0.23", "", ""},
	} {
		if got := a.internalURL(tt.raw, tt.host); got != tt.want {
			t.Errorf("%s / %s: got %q, want %q", tt.host, tt.raw, got, tt.want)
		}
	}
	for _, host := range []string{"home.example.test", "home-lan.example.test.evil.test", "10.0.0.1.example.test", "100.63.255.255", "100.128.0.1", "8.8.8.8", "127.0.0.1", "0.0.0.0", "169.254.1.1", "[::1]", "[fe80::1%eth0]", "[::ffff:10.0.0.1]", "[2001:db8::1]"} {
		if got := a.internalURL(raw, host); got != raw || a.defaultMode(host) != "external" {
			t.Errorf("unexpected internal host classification for %q: %q", host, got)
		}
	}
	if got := (AccessConfig{}).internalURL(raw, "10.24.0.5"); got != raw {
		t.Fatal("same-host replacement must be opt-in")
	}
}

func TestHomeHostConfigurationValidation(t *testing.T) {
	for _, host := range []string{"192.168.1.10", "https://home.test", "home.test:80", "home.test/path", "HOME.test", "home.test.", "localhost", "-home.test", "home_.test", "home..test", "home.test?x=1"} {
		if err := (AccessConfig{HomeHost: host}).validate(); err == nil {
			t.Errorf("invalid home hostname accepted: %q", host)
		}
	}
	for _, network := range []string{"10.0.0.1/8", "10.0.0.0/7", "0.0.0.0/0", "100.0.0.0/8", "100.128.0.0/10", "192.169.0.0/16", "::/0", "fe80::/10", "invalid"} {
		a := dynamicAccess()
		a.NetworkPorts = map[string][]int{network: {5001}}
		if err := a.validate(); err == nil {
			t.Errorf("invalid network accepted: %q", network)
		}
	}
	for _, ports := range [][]int{{0}, {65536}, {80, 80}} {
		a := dynamicAccess()
		a.DefaultPorts = ports
		if err := a.validate(); err == nil {
			t.Errorf("invalid default ports accepted: %v", ports)
		}
		a = dynamicAccess()
		a.NetworkPorts["10.0.0.0/8"] = ports
		if err := a.validate(); err == nil {
			t.Errorf("invalid network ports accepted: %v", ports)
		}
	}
	for _, a := range []AccessConfig{{DefaultPorts: []int{}}, {NetworkPorts: map[string][]int{"10.0.0.0/8": {80}}}} {
		if err := a.validate(); err == nil {
			t.Fatal("home_host must be supplied with dynamic port rules")
		}
	}
}

func TestDynamicAccessSurvivesSaveReloadAndClone(t *testing.T) {
	path := writeTempConfig(t, authTestConfig())
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, ports := range [][]int{nil, {}, {80, 5001}} {
		cfg.Access = dynamicAccess()
		cfg.Access.DefaultPorts = ports
		// Mixed legacy and dynamic configurations must both survive a save.
		cfg.Access.HostAliases = testAccess().HostAliases
		cfg.Access.AliasPorts = map[string][]int{"10.20.0.10": {80}}
		if err := SaveConfig(path, cfg); err != nil {
			t.Fatal(err)
		}
		reloaded, err := LoadConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(reloaded.Access, cfg.Access) {
			t.Fatalf("round trip changed port restrictions: %#v", reloaded.Access)
		}
		copy := cloneConfig(reloaded)
		if !reflect.DeepEqual(copy.Access, reloaded.Access) {
			t.Fatal("clone lost empty versus absent port policy")
		}
		if len(copy.Access.DefaultPorts) > 0 {
			copy.Access.DefaultPorts[0] = 443
		}
		copy.Access.NetworkPorts["100.64.0.0/10"][0] = 443
		copy.Access.NetworkPorts["192.168.20.0/24"] = []int{80}
		if !reflect.DeepEqual(reloaded.Access, cfg.Access) {
			t.Fatal("clone shares live port policy")
		}
		got := reloaded.Access.internalURL("http://home-lan.example.test:5001/", "10.99.0.5")
		if ports != nil && len(ports) == 0 {
			if got != "" {
				t.Fatal("empty policy must block direct access")
			}
		} else if got != "http://10.99.0.5:5001/" {
			t.Fatal("allowed policy must follow changed IP")
		}
	}
}

func TestDynamicNavigationAcrossRequestsAndEdits(t *testing.T) {
	path := writeTempConfig(t, authTestConfig())
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Access = dynamicAccess()
	const raw = "http://home-lan.example.test:5001/path?hostname=home-lan.example.test#view"
	cfg.Groups[0].Services[0].InternalURL = raw
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	srv, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"10.20.0.10", "10.80.0.99", "100.70.5.8", "100.122.34.9", "192.168.4.2", "172.16.9.3", "home-lan.example.test"} {
		base := "http://" + host + ":3002"
		login := httptest.NewRecorder()
		srv.ServeHTTP(login, httptest.NewRequest(http.MethodGet, base+"/login", nil))
		if !strings.Contains(login.Body.String(), `const defaultAccessMode = "internal";`) {
			t.Fatal("login default did not follow changing origin")
		}
		want := "http://" + host + ":5001/path?hostname=" + host + "#view"
		for _, endpoint := range []string{"/", "/api/navigation"} {
			rec := uiRequest(t, srv, http.MethodGet, base+endpoint, "")
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"internal_access_url":"`+want+`"`) {
				t.Fatalf("request %s: missing dynamic address", base+endpoint)
			}
		}
	}
	for _, next := range []string{raw, "https://other-lan.example.test/", "", "http://home-lan.example.test:8090/"} {
		body, _ := json.Marshal(ServiceUpdateRequest{Name: "App", InternalURL: next, ExternalURL: "https://app.example.test/", Health: ServiceHealthUpdateRequest{Type: "disabled"}})
		rec := uiRequest(t, srv, http.MethodPut, "http://100.120.0.99:3002/api/services/app", string(body))
		if rec.Code != http.StatusOK {
			t.Fatalf("edit failed: %s", rec.Body.String())
		}
		var result struct {
			Navigation navigationData `json:"navigation"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		item := result.Navigation.Groups[0].Services[0]
		if item.InternalURL != next || item.InternalAccessURL != cfg.Access.internalURL(next, "100.120.0.99") {
			t.Fatal("edit returned stale or overwritten source URL")
		}
		reloaded, err := LoadConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		if reloaded.Groups[0].Services[0].InternalURL != next || !reflect.DeepEqual(reloaded.Access, cfg.Access) {
			t.Fatal("editing changed stable configuration")
		}
	}
	content, _ := os.ReadFile(path)
	if strings.Contains(string(content), "100.120.0.99") || strings.Contains(string(content), "internal_access_url") {
		t.Fatal("request IP leaked into persistent config")
	}
	request := httptest.NewRequest(http.MethodGet, "http://home.example.test/api/navigation", nil)
	request.Header.Set("X-Forwarded-Host", "10.99.0.8:3002")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: srv.newSession("admin")})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, request)
	var data navigationData
	if err := json.Unmarshal(rec.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if data.DefaultAccessMode != "external" || data.Groups[0].Services[0].InternalAccessURL != "http://home-lan.example.test:8090/" {
		t.Fatal("forwarded host changed the requested origin")
	}
}
