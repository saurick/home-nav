package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var iconifyPartPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func (s *Server) handleIconifyIcon(w http.ResponseWriter, r *http.Request) {
	if !s.authenticated(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	collection, iconName, ok := parseIconifyPath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}

	cfg := s.currentConfig()
	body, err := loadIconifySVG(r.Context(), cfg.Assets.IconCacheDir, collection, iconName)
	if err != nil {
		http.Error(w, "图标加载失败", http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "private, max-age=604800")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body)
}

func parseIconifyPath(path string) (string, string, bool) {
	raw := strings.TrimPrefix(path, "/.iconify/")
	raw = strings.TrimSuffix(raw, ".svg")
	collection, iconName, ok := strings.Cut(raw, "/")
	if !ok {
		return "", "", false
	}
	collection, err := url.PathUnescape(collection)
	if err != nil {
		return "", "", false
	}
	iconName, err = url.PathUnescape(iconName)
	if err != nil {
		return "", "", false
	}
	if !iconifyPartPattern.MatchString(collection) || !iconifyPartPattern.MatchString(iconName) {
		return "", "", false
	}
	return collection, iconName, true
}

func loadIconifySVG(ctx context.Context, cacheDir, collection, iconName string) ([]byte, error) {
	if cacheDir != "" {
		body, ok, err := loadCachedIconifySVG(cacheDir, collection, iconName)
		if err != nil {
			return nil, err
		}
		if ok {
			return body, nil
		}
	}

	fetchCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	iconURL := fmt.Sprintf("https://api.iconify.design/%s/%s.svg?color=white", url.PathEscape(collection), url.PathEscape(iconName))
	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, iconURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("iconify status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return nil, err
	}
	if !strings.Contains(string(body), "<svg") {
		return nil, fmt.Errorf("invalid icon svg")
	}

	if cacheDir != "" {
		path := iconifyCachePath(cacheDir, collection, iconName)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err == nil {
			_ = os.WriteFile(path, body, 0600)
		}
	}
	return body, nil
}

func loadCachedIconifySVG(cacheDir, collection, iconName string) ([]byte, bool, error) {
	if cacheDir == "" {
		return nil, false, nil
	}
	path := iconifyCachePath(cacheDir, collection, iconName)
	body, err := os.ReadFile(path)
	if err == nil {
		return body, true, nil
	}
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	return nil, false, err
}

func iconifyCachePath(cacheDir, collection, iconName string) string {
	return filepath.Join(cacheDir, collection, iconName+".svg")
}
