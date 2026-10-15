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

// HostAliases describes alternate IPs of the same host, never a subnet-wide rewrite.
type AccessConfig struct {
	HostAliases map[string][]string `yaml:"host_aliases,omitempty"`
	AliasPorts  map[string][]int    `yaml:"alias_ports,omitempty"`
}

func (a AccessConfig) validate() error {
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
		seenPorts := make(map[int]bool)
		for _, port := range ports {
			if port < 1 || port > 65535 || seenPorts[port] {
				return fmt.Errorf("配置错误: access.alias_ports 必须使用不重复的 1-65535 端口: %q", alias)
			}
			seenPorts[port] = true
		}
	}
	return nil
}

func (a AccessConfig) requestAlias(requestHost string) (source, target string) {
	host := requestHost
	if value, _, err := net.SplitHostPort(host); err == nil {
		host = value
	}
	ip, err := netip.ParseAddr(strings.Trim(host, "[]"))
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
	if _, target := a.requestAlias(requestHost); target != "" {
		return "internal"
	}
	return "external"
}

func (a AccessConfig) internalURL(raw, requestHost string) string {
	source, target := a.requestAlias(requestHost)
	if target == "" || raw == "" {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() != source {
		return raw
	}
	if ports, limited := a.AliasPorts[target]; limited {
		port := 80
		if u.Scheme == "https" {
			port = 443
		}
		if u.Port() != "" {
			port, _ = strconv.Atoi(u.Port())
		}
		if !slices.Contains(ports, port) {
			return ""
		}
	}
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
		if ok && keyErr == nil && valueErr == nil && name == "hostname" && host == source {
			parts[i] = key + "=" + url.QueryEscape(target)
		}
	}
	u.RawQuery = strings.Join(parts, "&")
	return u.String()
}
