package server

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

type AccessConfig struct {
	// HomeHost is a stable DNS name identifying services on the navigation host.
	HomeHost     string           `yaml:"home_host,omitempty"`
	DefaultPorts []int            `yaml:"default_ports,omitempty"`
	NetworkPorts map[string][]int `yaml:"network_ports,omitempty"`
	// Explicit IP aliases remain supported for configurations without a stable name.
	HostAliases map[string][]string `yaml:"host_aliases,omitempty"`
	AliasPorts  map[string][]int    `yaml:"alias_ports,omitempty"`
}

func (a AccessConfig) validate() error {
	if a.HomeHost != "" {
		if !validHomeHost(a.HomeHost) {
			return fmt.Errorf("配置错误: access.home_host 必须是小写域名，不含协议、端口或路径")
		}
	} else if a.DefaultPorts != nil || len(a.NetworkPorts) > 0 {
		return fmt.Errorf("配置错误: access.default_ports 和 network_ports 需要 home_host")
	}
	if err := validateAccessPorts("default_ports", a.DefaultPorts); err != nil {
		return err
	}
	for network, ports := range a.NetworkPorts {
		prefix, err := netip.ParsePrefix(network)
		if err != nil || prefix != prefix.Masked() || prefix.String() != network || !internalNetwork(prefix) {
			return fmt.Errorf("配置错误: access.network_ports 必须使用规范的内网 CIDR: %q", network)
		}
		if err := validateAccessPorts(network, ports); err != nil {
			return err
		}
	}
	seen := make(map[string]bool)
	aliasHosts := make(map[string]bool)
	for source, aliases := range a.HostAliases {
		for _, alias := range aliases {
			aliasHosts[alias] = true
		}
		for _, host := range append([]string{source}, aliases...) {
			ip, err := netip.ParseAddr(host)
			if err != nil || ip.Zone() != "" || ip.String() != host || ip.IsUnspecified() || ip.IsMulticast() {
				return fmt.Errorf("配置错误: access.host_aliases 必须使用不含端口的规范 IP 地址: %q", host)
			}
			if seen[host] {
				return fmt.Errorf("配置错误: access.host_aliases 地址 %q 重复", host)
			}
			seen[host] = true
		}
	}
	for alias, ports := range a.AliasPorts {
		if !aliasHosts[alias] {
			return fmt.Errorf("配置错误: access.alias_ports 地址 %q 未在 host_aliases 中声明", alias)
		}
		if err := validateAccessPorts(alias, ports); err != nil {
			return err
		}
	}
	return nil
}

var internalNetworks = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("fc00::/7"),
}

func internalNetwork(prefix netip.Prefix) bool {
	return slices.ContainsFunc(internalNetworks, func(network netip.Prefix) bool {
		return network.Addr().BitLen() == prefix.Addr().BitLen() && network.Bits() <= prefix.Bits() && network.Contains(prefix.Addr())
	})
}

func validHomeHost(host string) bool {
	if len(host) > 253 || !strings.Contains(host, ".") || strings.ToLower(host) != host {
		return false
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func validateAccessPorts(name string, ports []int) error {
	seen := make(map[int]bool)
	for _, port := range ports {
		if port < 1 || port > 65535 || seen[port] {
			return fmt.Errorf("配置错误: access 端口必须为不重复的 1-65535 整数: %q", name)
		}
		seen[port] = true
	}
	return nil
}

func accessRequestHost(host string) string {
	if value, _, err := net.SplitHostPort(host); err == nil {
		host = value
	}
	return strings.Trim(host, "[]")
}

func (a AccessConfig) homeTarget(requestHost string) string {
	if a.HomeHost == "" {
		return ""
	}
	host := accessRequestHost(requestHost)
	if strings.EqualFold(strings.TrimSuffix(host, "."), a.HomeHost) {
		return a.HomeHost
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || ip.Zone() != "" || ip.Is4In6() {
		return ""
	}
	if internalNetwork(netip.PrefixFrom(ip, ip.BitLen())) {
		return ip.String()
	}
	return ""
}

func (a AccessConfig) requestAlias(requestHost string) (source, target string) {
	ip, err := netip.ParseAddr(accessRequestHost(requestHost))
	if err != nil {
		return "", ""
	}
	for source, aliases := range a.HostAliases {
		for _, alias := range aliases {
			if ip.String() == alias {
				return source, alias
			}
		}
	}
	return "", ""
}

func (a AccessConfig) defaultMode(requestHost string) string {
	if a.homeTarget(requestHost) != "" {
		return "internal"
	}
	if _, target := a.requestAlias(requestHost); target != "" {
		return "internal"
	}
	return "external"
}

func (a AccessConfig) internalURL(raw, requestHost string) string {
	u, err := url.Parse(raw)
	if raw == "" || err != nil {
		return raw
	}
	// The stable hostname identifies the machine. An arbitrary private service URL
	// never becomes a same-host URL merely because its IP shares a network prefix.
	if target := a.homeTarget(requestHost); target != "" && strings.EqualFold(u.Hostname(), a.HomeHost) {
		ports := a.DefaultPorts
		limited := ports != nil
		ip, _ := netip.ParseAddr(target)
		best := -1
		for network, allowed := range a.NetworkPorts {
			prefix, err := netip.ParsePrefix(network)
			if err == nil && prefix.Contains(ip) && prefix.Bits() > best {
				ports, limited, best = allowed, true, prefix.Bits()
			}
		}
		if limited && !allowsURLPort(ports, u) {
			return ""
		}
		return replaceAccessHost(u, a.HomeHost, target)
	}
	source, target := a.requestAlias(requestHost)
	if target == "" || u.Hostname() != source {
		return raw
	}
	if ports, limited := a.AliasPorts[target]; limited && !allowsURLPort(ports, u) {
		return ""
	}
	return replaceAccessHost(u, source, target)
}

func allowsURLPort(ports []int, u *url.URL) bool {
	port := 80
	if u.Scheme == "https" {
		port = 443
	}
	if u.Port() != "" {
		port, _ = strconv.Atoi(u.Port())
	}
	return slices.Contains(ports, port)
}

func replaceAccessHost(u *url.URL, source, target string) string {
	if port := u.Port(); port != "" {
		u.Host = net.JoinHostPort(target, port)
	} else if strings.Contains(target, ":") {
		u.Host = "[" + target + "]"
	} else {
		u.Host = target
	}
	// Proxy dashboards also carry the controller host in this parameter.
	// Replace only an exact same-host value; other query values remain untouched.
	parts := strings.Split(u.RawQuery, "&")
	for i, part := range parts {
		key, value, ok := strings.Cut(part, "=")
		name, keyErr := url.QueryUnescape(key)
		host, valueErr := url.QueryUnescape(value)
		if ok && keyErr == nil && valueErr == nil && name == "hostname" && strings.EqualFold(host, source) {
			parts[i] = key + "=" + url.QueryEscape(target)
		}
	}
	u.RawQuery = strings.Join(parts, "&")
	return u.String()
}
