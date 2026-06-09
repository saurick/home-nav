package server

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

type Server struct {
	mu         sync.RWMutex
	configPath string
	cfg        *Config
	statuses   *StatusCache
	indexTpl   *template.Template
	loginTpl   *template.Template
	mux        *http.ServeMux
}

type PageData struct {
	Title         string
	Subtitle      string
	Groups        []Group
	Tags          []string
	Auth          AuthConfig
	Appearance    Appearance
	BackgroundCSS template.CSS
}

type LoginData struct {
	Title    string
	Error    string
	ReturnTo string
}

type ServiceUpdateRequest struct {
	Name        string                     `json:"name"`
	Description string                     `json:"description"`
	IconText    string                     `json:"icon_text"`
	Icon        string                     `json:"icon"`
	InternalURL string                     `json:"internal_url"`
	ExternalURL string                     `json:"external_url"`
	Tags        []string                   `json:"tags"`
	Notes       string                     `json:"notes"`
	GroupID     string                     `json:"group_id"`
	Health      ServiceHealthUpdateRequest `json:"health"`
}

type ServiceSortRequest struct {
	Groups []ServiceSortGroupRequest `json:"groups"`
}

type ServiceSortGroupRequest struct {
	GroupID    string   `json:"group_id"`
	ServiceIDs []string `json:"service_ids"`
}

type AppearanceUpdateRequest struct {
	BackgroundColor string `json:"background_color"`
	BackgroundImage string `json:"background_image"`
}

type ServiceHealthUpdateRequest struct {
	Type         string `json:"type"`
	URL          string `json:"url"`
	Address      string `json:"address"`
	ExpectStatus int    `json:"expect_status"`
	Timeout      string `json:"timeout"`
}

func New(configPath string) (*Server, error) {
	cfg, err := LoadConfig(configPath)
	if err != nil {
		return nil, err
	}

	indexTpl, err := template.New("index").Parse(indexTemplate)
	if err != nil {
		return nil, err
	}
	loginTpl, err := template.New("login").Parse(loginTemplate)
	if err != nil {
		return nil, err
	}

	s := &Server{
		configPath: configPath,
		cfg:        cfg,
		statuses:   NewStatusCache(cfg),
		indexTpl:   indexTpl,
		loginTpl:   loginTpl,
		mux:        http.NewServeMux(),
	}
	s.routes()
	s.statuses.Start(context.Background(), cfg.CheckInterval)
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	s.mux.HandleFunc("/healthz", s.handleHealthz)
	s.mux.HandleFunc("/favicon.svg", handleFavicon)
	s.mux.HandleFunc("/favicon.ico", handleFavicon)
	s.mux.HandleFunc("/login", s.handleLogin)
	s.mux.HandleFunc("/logout", s.handleLogout)
	s.mux.HandleFunc("/api/status", s.handleStatus)
	s.mux.HandleFunc("/api/services/sort", s.handleServiceSort)
	s.mux.HandleFunc("/api/services", s.handleServices)
	s.mux.HandleFunc("/api/services/", s.handleService)
	s.mux.HandleFunc("/api/settings", s.handleSettings)
	s.mux.HandleFunc("/api/uploads", s.handleUpload)
	s.mux.HandleFunc("/.iconify/", s.handleIconifyIcon)
	if s.cfg.Assets.UploadsDir != "" {
		s.mux.Handle(s.cfg.Assets.UploadsURLPrefix, http.StripPrefix(s.cfg.Assets.UploadsURLPrefix, http.FileServer(http.Dir(s.cfg.Assets.UploadsDir))))
	}
	s.mux.HandleFunc("/", s.handleIndex)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func handleFavicon(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write([]byte(faviconSVG))
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if !s.authenticated(r) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"请先登录"}` + "\n"))
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(s.statuses.Snapshot())
}

func (s *Server) handleServices(w http.ResponseWriter, r *http.Request) {
	if !s.authenticated(r) {
		writeJSONError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "请求方法不支持")
		return
	}

	var payload ServiceUpdateRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&payload); err != nil {
		writeJSONError(w, http.StatusBadRequest, "请求内容无效")
		return
	}

	cfg, service, err := s.createService(payload)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := SaveConfig(s.configPath, cfg); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
	s.statuses.UpdateConfig(cfg)

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"service": service})
}

func (s *Server) handleServiceSort(w http.ResponseWriter, r *http.Request) {
	if !s.authenticated(r) {
		writeJSONError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	if r.Method != http.MethodPut {
		writeJSONError(w, http.StatusMethodNotAllowed, "请求方法不支持")
		return
	}

	var payload ServiceSortRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&payload); err != nil {
		writeJSONError(w, http.StatusBadRequest, "请求内容无效")
		return
	}

	cfg, err := s.sortServices(payload)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := SaveConfig(s.configPath, cfg); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
	s.statuses.UpdateConfig(cfg)

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func (s *Server) handleService(w http.ResponseWriter, r *http.Request) {
	if !s.authenticated(r) {
		writeJSONError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	if r.Method != http.MethodPut && r.Method != http.MethodDelete {
		writeJSONError(w, http.StatusMethodNotAllowed, "请求方法不支持")
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/api/services/")
	if id == "" || strings.Contains(id, "/") {
		writeJSONError(w, http.StatusNotFound, "服务不存在")
		return
	}

	if r.Method == http.MethodDelete {
		cfg, err := s.deleteService(id)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := SaveConfig(s.configPath, cfg); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		s.mu.Lock()
		s.cfg = cfg
		s.mu.Unlock()
		s.statuses.UpdateConfig(cfg)

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{"deleted": id})
		return
	}

	var payload ServiceUpdateRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&payload); err != nil {
		writeJSONError(w, http.StatusBadRequest, "请求内容无效")
		return
	}

	cfg, service, err := s.updateService(id, payload)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := SaveConfig(s.configPath, cfg); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
	s.statuses.UpdateConfig(cfg)

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"service": service,
	})
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	if !s.authenticated(r) {
		writeJSONError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	if r.Method != http.MethodPut {
		writeJSONError(w, http.StatusMethodNotAllowed, "请求方法不支持")
		return
	}

	var payload AppearanceUpdateRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&payload); err != nil {
		writeJSONError(w, http.StatusBadRequest, "请求内容无效")
		return
	}

	cfg, err := s.updateAppearance(payload)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := SaveConfig(s.configPath, cfg); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"appearance": cfg.Appearance})
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if !s.authenticated(r) {
		redirectToLogin(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	cfg := s.currentConfig()
	_ = s.indexTpl.Execute(w, PageData{
		Title:         cfg.Title,
		Subtitle:      cfg.Subtitle,
		Groups:        cfg.Groups,
		Tags:          collectTags(cfg.Groups),
		Auth:          cfg.Auth,
		Appearance:    cfg.Appearance,
		BackgroundCSS: backgroundCSS(cfg.Appearance),
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.authEnabled() {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	returnTo := cleanReturnTo(r.URL.Query().Get("return_to"))
	switch r.Method {
	case http.MethodGet:
		if s.authenticated(r) {
			http.Redirect(w, r, returnTo, http.StatusSeeOther)
			return
		}
		cfg := s.currentConfig()
		s.renderLogin(w, LoginData{Title: cfg.Title, ReturnTo: returnTo})
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			cfg := s.currentConfig()
			s.renderLogin(w, LoginData{Title: cfg.Title, Error: "登录请求无效", ReturnTo: returnTo})
			return
		}
		if !s.validCredentials(r.FormValue("username"), r.FormValue("password")) {
			cfg := s.currentConfig()
			s.renderLogin(w, LoginData{Title: cfg.Title, Error: "账号或密码不正确", ReturnTo: returnTo})
			return
		}
		cfg := s.currentConfig()
		s.setSessionCookie(w, r, cfg.Auth.Username)
		http.Redirect(w, r, returnTo, http.StatusSeeOther)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	clearSessionCookie(w, r)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) renderLogin(w http.ResponseWriter, data LoginData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.loginTpl.Execute(w, data)
}

func redirectToLogin(w http.ResponseWriter, r *http.Request) {
	target := "/login"
	if r.URL.RequestURI() != "/" {
		target += "?return_to=" + url.QueryEscape(r.URL.RequestURI())
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func cleanReturnTo(value string) string {
	if value == "" || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") {
		return "/"
	}
	return value
}

func (s *Server) currentConfig() *Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

func (s *Server) createService(payload ServiceUpdateRequest) (*Config, Service, error) {
	s.mu.RLock()
	cfg := cloneConfig(s.cfg)
	s.mu.RUnlock()

	targetGroup := -1
	if payload.GroupID != "" {
		for i, group := range cfg.Groups {
			if group.ID == payload.GroupID {
				targetGroup = i
				break
			}
		}
		if targetGroup < 0 {
			return nil, Service{}, fmt.Errorf("分组不存在")
		}
	}
	if targetGroup < 0 && len(cfg.Groups) > 0 {
		targetGroup = 0
	}
	if targetGroup < 0 {
		return nil, Service{}, fmt.Errorf("分组不存在")
	}

	service := Service{
		ID:          uniqueServiceID(cfg, payload.Name),
		Name:        payload.Name,
		Description: payload.Description,
		IconText:    payload.IconText,
		Icon:        payload.Icon,
		InternalURL: payload.InternalURL,
		ExternalURL: payload.ExternalURL,
		Tags:        payload.Tags,
		Notes:       payload.Notes,
		Health: HealthCheck{
			Type:         payload.Health.Type,
			URL:          payload.Health.URL,
			Address:      payload.Health.Address,
			ExpectStatus: payload.Health.ExpectStatus,
		},
	}
	if service.Health.Type == "" {
		service.Health.Type = "disabled"
	}
	if payload.Health.Timeout != "" {
		timeout, err := time.ParseDuration(payload.Health.Timeout)
		if err != nil {
			return nil, Service{}, fmt.Errorf("health.timeout 格式无效")
		}
		service.Health.Timeout = timeout
	}
	cfg.Groups[targetGroup].Services = append(cfg.Groups[targetGroup].Services, service)
	if err := cfg.NormalizeAndValidate(); err != nil {
		return nil, Service{}, err
	}
	for _, candidate := range cfg.Groups[targetGroup].Services {
		if candidate.ID == service.ID {
			return cfg, candidate, nil
		}
	}
	return nil, Service{}, fmt.Errorf("服务新增失败")
}

func (s *Server) updateService(id string, payload ServiceUpdateRequest) (*Config, Service, error) {
	s.mu.RLock()
	cfg := cloneConfig(s.cfg)
	s.mu.RUnlock()

	fromGroup, serviceIndex := findService(cfg, id)
	if fromGroup < 0 {
		return nil, Service{}, fmt.Errorf("服务不存在")
	}

	service := cfg.Groups[fromGroup].Services[serviceIndex]
	service.Name = payload.Name
	service.Description = payload.Description
	service.IconText = payload.IconText
	service.Icon = payload.Icon
	service.InternalURL = payload.InternalURL
	service.ExternalURL = payload.ExternalURL
	service.Tags = payload.Tags
	service.Notes = payload.Notes
	service.Health = HealthCheck{
		Type:         payload.Health.Type,
		URL:          payload.Health.URL,
		Address:      payload.Health.Address,
		ExpectStatus: payload.Health.ExpectStatus,
	}
	if payload.Health.Timeout != "" {
		timeout, err := time.ParseDuration(payload.Health.Timeout)
		if err != nil {
			return nil, Service{}, fmt.Errorf("health.timeout 格式无效")
		}
		service.Health.Timeout = timeout
	}

	targetGroup := fromGroup
	if payload.GroupID != "" {
		for i, group := range cfg.Groups {
			if group.ID == payload.GroupID {
				targetGroup = i
				break
			}
		}
		if cfg.Groups[targetGroup].ID != payload.GroupID {
			return nil, Service{}, fmt.Errorf("分组不存在")
		}
	}

	if targetGroup != fromGroup {
		cfg.Groups[fromGroup].Services = append(cfg.Groups[fromGroup].Services[:serviceIndex], cfg.Groups[fromGroup].Services[serviceIndex+1:]...)
		cfg.Groups[targetGroup].Services = append(cfg.Groups[targetGroup].Services, service)
	} else {
		cfg.Groups[fromGroup].Services[serviceIndex] = service
	}

	if err := cfg.NormalizeAndValidate(); err != nil {
		return nil, Service{}, err
	}
	_, serviceIndex = findService(cfg, id)
	if serviceIndex < 0 {
		return nil, Service{}, fmt.Errorf("服务更新失败")
	}
	for _, group := range cfg.Groups {
		for _, candidate := range group.Services {
			if candidate.ID == id {
				return cfg, candidate, nil
			}
		}
	}
	return nil, Service{}, fmt.Errorf("服务更新失败")
}

func (s *Server) deleteService(id string) (*Config, error) {
	s.mu.RLock()
	cfg := cloneConfig(s.cfg)
	s.mu.RUnlock()

	groupIndex, serviceIndex := findService(cfg, id)
	if groupIndex < 0 {
		return nil, fmt.Errorf("服务不存在")
	}

	services := cfg.Groups[groupIndex].Services
	cfg.Groups[groupIndex].Services = append(services[:serviceIndex], services[serviceIndex+1:]...)
	if err := cfg.NormalizeAndValidate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (s *Server) sortServices(payload ServiceSortRequest) (*Config, error) {
	s.mu.RLock()
	cfg := cloneConfig(s.cfg)
	s.mu.RUnlock()

	if len(payload.Groups) != len(cfg.Groups) {
		return nil, fmt.Errorf("排序数据必须包含全部分组")
	}

	groupIndex := make(map[string]int, len(cfg.Groups))
	serviceByID := make(map[string]Service)
	for gi, group := range cfg.Groups {
		groupIndex[group.ID] = gi
		for _, service := range group.Services {
			serviceByID[service.ID] = service
		}
	}

	groupSeen := make(map[string]struct{}, len(cfg.Groups))
	serviceSeen := make(map[string]struct{}, len(serviceByID))
	nextServices := make([][]Service, len(cfg.Groups))
	for _, groupOrder := range payload.Groups {
		groupID := strings.TrimSpace(groupOrder.GroupID)
		gi, ok := groupIndex[groupID]
		if !ok {
			return nil, fmt.Errorf("分组不存在")
		}
		if _, ok := groupSeen[groupID]; ok {
			return nil, fmt.Errorf("排序数据包含重复分组")
		}
		groupSeen[groupID] = struct{}{}
		for _, rawServiceID := range groupOrder.ServiceIDs {
			serviceID := strings.TrimSpace(rawServiceID)
			service, ok := serviceByID[serviceID]
			if !ok {
				return nil, fmt.Errorf("服务不存在")
			}
			if _, ok := serviceSeen[serviceID]; ok {
				return nil, fmt.Errorf("排序数据包含重复服务")
			}
			serviceSeen[serviceID] = struct{}{}
			nextServices[gi] = append(nextServices[gi], service)
		}
	}
	if len(groupSeen) != len(cfg.Groups) {
		return nil, fmt.Errorf("排序数据必须包含全部分组")
	}
	if len(serviceSeen) != len(serviceByID) {
		return nil, fmt.Errorf("排序数据必须包含全部服务")
	}

	for gi := range cfg.Groups {
		cfg.Groups[gi].Services = nextServices[gi]
	}
	if err := cfg.NormalizeAndValidate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (s *Server) updateAppearance(payload AppearanceUpdateRequest) (*Config, error) {
	s.mu.RLock()
	cfg := cloneConfig(s.cfg)
	s.mu.RUnlock()

	cfg.Appearance.BackgroundColor = payload.BackgroundColor
	cfg.Appearance.BackgroundImage = payload.BackgroundImage
	if err := cfg.NormalizeAndValidate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func findService(cfg *Config, id string) (int, int) {
	for gi, group := range cfg.Groups {
		for si, service := range group.Services {
			if service.ID == id {
				return gi, si
			}
		}
	}
	return -1, -1
}

func cloneConfig(cfg *Config) *Config {
	clone := *cfg
	clone.Groups = make([]Group, len(cfg.Groups))
	for gi, group := range cfg.Groups {
		clone.Groups[gi] = group
		clone.Groups[gi].Services = make([]Service, len(group.Services))
		for si, service := range group.Services {
			clone.Groups[gi].Services[si] = service
			clone.Groups[gi].Services[si].Tags = append([]string(nil), service.Tags...)
		}
	}
	return &clone
}

func uniqueServiceID(cfg *Config, name string) string {
	base := slugifyID(name)
	if base == "" {
		base = "service"
	}
	used := make(map[string]struct{})
	for _, group := range cfg.Groups {
		for _, service := range group.Services {
			used[service.ID] = struct{}{}
		}
	}
	if _, ok := used[base]; !ok {
		return base
	}
	for i := 2; ; i++ {
		id := fmt.Sprintf("%s-%d", base, i)
		if _, ok := used[id]; !ok {
			return id
		}
	}
}

func slugifyID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteString(fmt.Sprintf("%x", r))
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

var cssEscapePattern = regexp.MustCompile(`["\\\n\r]`)

func backgroundCSS(appearance Appearance) template.CSS {
	color := appearance.BackgroundColor
	if color == "" {
		color = "#000000"
	}
	var b strings.Builder
	b.WriteString("background-color:")
	b.WriteString(color)
	b.WriteString(";")
	if appearance.BackgroundImage != "" {
		image := cssEscapePattern.ReplaceAllString(appearance.BackgroundImage, "")
		b.WriteString("background-image:linear-gradient(rgba(0,0,0,.18),rgba(0,0,0,.18)),url(\"")
		b.WriteString(image)
		b.WriteString("\");background-size:cover;background-position:center;background-attachment:fixed;")
	}
	return template.CSS(b.String())
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func collectTags(groups []Group) []string {
	seen := make(map[string]struct{})
	for _, group := range groups {
		for _, service := range group.Services {
			for _, tag := range service.Tags {
				seen[tag] = struct{}{}
			}
		}
	}

	tags := make([]string, 0, len(seen))
	for tag := range seen {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	return tags
}

const indexTemplate = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{.Title}}</title>
  <link rel="icon" href="/favicon.svg" type="image/svg+xml">
  <script src="https://code.iconify.design/iconify-icon/2.1.0/iconify-icon.min.js" defer></script>
  <style>
    :root {
      color-scheme: dark;
      --bg: #000;
      --panel: #151515;
      --panel-2: #202026;
      --panel-3: #2c2c34;
      --text: #f7f7f7;
      --muted: #b5bac5;
      --line: #4c4d56;
      --accent: #67e0b6;
      --danger: #e77d82;
      --ok: #67e0b6;
      --bad: #ff8c8c;
      --unknown: #e5c46b;
      --disabled: #979ba6;
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", sans-serif;
    }
    * { box-sizing: border-box; }
    body { margin: 0; min-height: 100vh; background: var(--bg); background-repeat: no-repeat; color: var(--text); }
    a { color: inherit; text-decoration: none; }
    button, input, textarea, select { font: inherit; }
    .shell { width: min(1240px, calc(100vw - 36px)); margin: 0 auto; padding: 52px 0 80px; }
    .top-tools { position: fixed; top: 22px; right: 22px; display: flex; gap: 10px; z-index: 20; }
    .top-tools form { margin: 0; }
	    .tool-button { width: 48px; height: 48px; border: 0; border-radius: 8px; background: #141414; color: #fff; display: grid; place-items: center; cursor: pointer; }
	    .tool-button:hover { background: #242424; }
	    .tool-button:disabled { cursor: default; opacity: .42; }
	    .tool-button:disabled:hover { background: #141414; }
	    .sort-button { display: none; }
	    body.is-edit-mode .sort-button { display: grid; }
	    .tool-button iconify-icon { font-size: 22px; }
    .hero { display: grid; justify-items: center; gap: 22px; margin-bottom: 52px; }
    .title-row { display: flex; align-items: flex-end; justify-content: center; gap: 14px; flex-wrap: wrap; }
    h1 { margin: 0; font-size: clamp(44px, 6vw, 72px); line-height: .95; letter-spacing: 0; font-weight: 800; }
    .clock { display: grid; gap: 4px; padding-bottom: 6px; }
    .clock-time { font-size: clamp(24px, 3vw, 36px); line-height: 1; font-weight: 800; }
    .clock-date { color: var(--muted); font-size: 16px; }
    .search-wrap { width: min(806px, 100%); height: 50px; border: 1px solid #b8bcc6; border-radius: 18px; display: grid; grid-template-columns: 42px 1fr; align-items: center; padding: 0 14px; }
    .search-wrap iconify-icon { color: #fff; font-size: 24px; }
    .search { width: 100%; min-width: 0; border: 0; outline: 0; background: transparent; color: var(--text); font-size: 18px; }
    .search::placeholder { color: #9ea4b0; }
    .groups { display: grid; gap: 56px; }
    .group { display: grid; gap: 24px; }
    .group-title { display: flex; align-items: center; justify-content: space-between; gap: 16px; }
    .group-heading { display: flex; align-items: baseline; gap: 12px; min-width: 0; }
    h2 { margin: 0; font-size: 24px; letter-spacing: 0; font-weight: 800; }
    .group-count { color: var(--muted); font-size: 14px; }
    .group-actions { display: flex; align-items: center; gap: 8px; }
    .group-action { width: 38px; height: 38px; border: 1px solid transparent; border-radius: 8px; background: transparent; color: #fff; display: grid; place-items: center; cursor: pointer; }
    .group-action:hover, .group-action.is-active { border-color: rgba(255,255,255,.35); background: rgba(255,255,255,.08); }
    .group-action iconify-icon { font-size: 26px; }
	    .icon-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(86px, 1fr)); gap: 28px 20px; align-items: start; }
	    .app-icon { display: grid; justify-items: center; gap: 8px; min-width: 0; color: #fff; }
	    body.is-edit-mode .app-icon { cursor: grab; touch-action: none; }
	    body.is-dragging, body.is-dragging * { user-select: none; }
	    body.is-dragging .app-icon { cursor: grabbing; }
	    .app-icon.is-dragging { position: fixed; left: 0; top: 0; z-index: 70; opacity: .96; pointer-events: none; filter: drop-shadow(0 22px 34px rgba(0,0,0,.46)); transform: translate3d(0,0,0); will-change: transform; }
	    body.is-dragging .app-icon.is-dragging .icon-button { transform: none; background: #242424; }
	    .drag-placeholder { width: 100%; min-height: 122px; border-radius: 16px; outline: 2px dashed rgba(103,224,182,.38); outline-offset: -6px; background: rgba(103,224,182,.08); }
	    .icon-button { width: 76px; height: 76px; border: 0; border-radius: 14px; background: var(--panel); color: #fff; display: grid; place-items: center; cursor: pointer; transition: transform .12s ease, background .12s ease; position: relative; }
    .icon-button:hover { transform: translateY(-2px); background: #1f1f1f; }
    body.is-edit-mode .icon-button { outline: 2px dashed rgba(255,255,255,.55); outline-offset: 5px; }
    .icon-button:focus-visible { outline: 3px solid rgba(103, 224, 182, .45); outline-offset: 3px; }
    .icon-button iconify-icon { font-size: 42px; }
    .icon-button img { width: 50px; height: 50px; object-fit: contain; border-radius: 10px; }
    .icon-fallback { font-size: 17px; font-weight: 800; max-width: 64px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .health-dot { position: absolute; right: 7px; bottom: 7px; width: 10px; height: 10px; border-radius: 50%; background: var(--unknown); box-shadow: 0 0 0 2px var(--panel); }
    .health-dot[data-status="healthy"] { background: var(--ok); }
    .health-dot[data-status="unhealthy"] { background: var(--bad); }
    .health-dot[data-status="disabled"] { background: var(--disabled); }
    .app-name { width: 92px; min-height: 38px; text-align: center; color: #fff; font-size: 15px; line-height: 1.35; overflow-wrap: anywhere; }
    .empty { display: none; color: var(--muted); padding: 22px 0; }
    body.is-empty .empty { display: block; }
    .group.is-hidden, .app-icon.is-hidden { display: none; }
    .menu { position: fixed; z-index: 50; min-width: 292px; border-radius: 6px; background: #53535d; box-shadow: 0 18px 48px rgba(0,0,0,.45); padding: 20px 0 10px; display: none; }
    .menu.is-open { display: block; }
    .menu-section { padding: 0 24px 16px; }
    .menu-title { margin: 0 0 8px; color: #e8e8ef; font-size: 24px; font-weight: 700; }
    .menu-actions { display: flex; gap: 10px; }
    .menu-icon { width: 52px; height: 44px; border: 1px solid #82828b; border-radius: 5px; background: transparent; color: #f4f4f7; display: grid; place-items: center; cursor: pointer; }
    .menu-icon iconify-icon { font-size: 24px; }
    .menu-line { height: 1px; background: #696973; margin: 6px 0; }
    .menu-command { width: 100%; min-height: 54px; border: 0; background: transparent; color: #f4f4f7; display: flex; align-items: center; gap: 16px; padding: 0 24px; font-size: 22px; cursor: pointer; }
    .menu-command:hover, .menu-icon:hover { background: rgba(255,255,255,.08); }
    .menu-command iconify-icon { font-size: 28px; }
    .modal-backdrop { position: fixed; inset: 0; display: none; place-items: center; background: rgba(0,0,0,.62); z-index: 60; padding: 18px; }
    .modal-backdrop.is-open { display: grid; }
    .modal { width: min(1100px, 100%); max-height: min(860px, calc(100vh - 36px)); overflow: auto; border-radius: 28px; background: #2d2d35; color: #f7f7fb; box-shadow: 0 22px 80px rgba(0,0,0,.62); padding: 28px 32px 32px; }
    .confirm-backdrop { z-index: 80; background: rgba(0,0,0,.72); }
    .confirm-modal { width: min(480px, 100%); max-height: calc(100vh - 36px); overflow: auto; border-radius: 18px; background: #25252d; border: 1px solid #595a64; color: #f7f7fb; box-shadow: 0 26px 90px rgba(0,0,0,.68); padding: 26px; }
    .confirm-head { display: flex; align-items: center; gap: 14px; margin-bottom: 16px; }
    .confirm-mark { width: 46px; height: 46px; border-radius: 12px; background: rgba(231,125,130,.14); color: var(--danger); display: grid; place-items: center; flex: 0 0 auto; }
    .confirm-mark iconify-icon { font-size: 26px; }
    .confirm-title { margin: 0; font-size: 24px; line-height: 1.2; letter-spacing: 0; }
    .confirm-body { margin: 0 0 8px; color: #e6e7ec; font-size: 17px; line-height: 1.55; }
    .confirm-name { color: #fff; font-weight: 800; overflow-wrap: anywhere; }
    .confirm-note { margin: 0; color: var(--muted); font-size: 14px; line-height: 1.5; }
    .confirm-actions { display: flex; justify-content: flex-end; gap: 12px; margin-top: 24px; }
    .confirm-cancel, .confirm-delete { min-width: 106px; min-height: 48px; border: 0; border-radius: 8px; padding: 0 18px; font-size: 18px; font-weight: 800; cursor: pointer; }
    .confirm-cancel { background: #454650; color: #f1f2f6; }
    .confirm-cancel:hover { background: #555762; }
    .confirm-delete { background: var(--danger); color: #050505; }
    .confirm-delete:hover { background: #ff969b; }
    .confirm-cancel:focus-visible, .confirm-delete:focus-visible { outline: 3px solid rgba(103,224,182,.42); outline-offset: 3px; }
    .confirm-delete:disabled { cursor: default; opacity: .62; }
    .modal-head { display: flex; justify-content: space-between; align-items: center; gap: 18px; margin-bottom: 18px; }
    .modal-title { margin: 0; font-size: 30px; }
    .close-button { width: 44px; height: 44px; border: 0; border-radius: 7px; background: #565660; color: #ddd; cursor: pointer; display: grid; place-items: center; }
    .close-button iconify-icon { font-size: 30px; }
    .preview { background: #c2c8d0; border: 1px solid #9fa5ad; border-radius: 22px; min-height: 180px; display: grid; grid-template-columns: 1fr 220px; gap: 28px; align-items: center; padding: 18px 26px; margin-bottom: 26px; color: #0b0b0f; }
    .preview-wide { justify-self: end; width: min(420px, 100%); height: 140px; border-radius: 28px; background: #888d90; display: flex; align-items: center; justify-content: center; gap: 34px; color: #d9dde0; font-size: 28px; font-weight: 800; }
    .preview-square { width: 140px; display: grid; gap: 10px; justify-items: center; font-size: 28px; color: #050506; }
    .preview-icon { width: 140px; height: 140px; border-radius: 28px; background: #878c8f; display: grid; place-items: center; color: #d9dde0; }
    .preview-icon iconify-icon { font-size: 62px; }
    .preview-icon img { width: 92px; height: 92px; object-fit: contain; }
    .form-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 22px 20px; }
    .field { display: grid; gap: 8px; min-width: 0; }
    .field.full { grid-column: 1 / -1; }
    .field label { font-size: 20px; font-weight: 800; color: #f0f0f4; }
    .field-link { margin-left: 10px; color: #83d8ff; font-size: 18px; font-weight: 600; }
    .field input, .field textarea, .field select { width: 100%; min-height: 56px; border: 1px solid transparent; border-radius: 6px; background: #48484f; color: #f3f3f7; padding: 0 18px; font-size: 20px; outline: 0; }
    .field textarea { min-height: 92px; padding-top: 14px; resize: vertical; line-height: 1.45; }
    .field input:focus, .field textarea:focus, .field select:focus { border-color: var(--accent); box-shadow: 0 0 0 3px rgba(103,224,182,.15); }
    .icon-field-row { display: grid; grid-template-columns: 1fr auto; gap: 10px; align-items: center; }
    .upload-button { min-height: 56px; border: 1px solid #73737d; border-radius: 6px; background: #3d3d45; color: #f3f3f7; padding: 0 18px; cursor: pointer; white-space: nowrap; }
    .upload-button:hover { background: #50505a; }
    .file-input { position: fixed; opacity: 0; pointer-events: none; width: 1px; height: 1px; }
    .form-actions { display: flex; justify-content: flex-end; gap: 14px; margin-top: 30px; }
    .save-button, .delete-button { min-width: 118px; min-height: 58px; border: 0; border-radius: 7px; font-size: 24px; cursor: pointer; }
    .save-button { background: var(--accent); color: #050505; }
    .delete-button { background: var(--danger); color: #050505; display: none; }
    .delete-button.is-visible { display: inline-block; }
    .settings-preview { min-height: 170px; border: 1px solid #676873; border-radius: 16px; background: #000; display: grid; place-items: center; color: #fff; margin-bottom: 24px; overflow: hidden; }
    .settings-preview span { padding: 10px 16px; border-radius: 999px; background: rgba(0,0,0,.58); color: #fff; }
    .color-row { display: grid; grid-template-columns: 86px 1fr; gap: 12px; align-items: center; }
    .field input[type="color"] { min-height: 56px; padding: 6px; cursor: pointer; }
    .secondary-button { min-height: 58px; border: 1px solid #73737d; border-radius: 7px; background: #3d3d45; color: #f3f3f7; padding: 0 18px; font-size: 22px; cursor: pointer; }
    .secondary-button:hover { background: #50505a; }
    .toast { position: fixed; left: 50%; bottom: 28px; transform: translateX(-50%); background: #202026; color: #fff; border: 1px solid #555761; border-radius: 999px; padding: 10px 18px; display: none; z-index: 80; }
    .toast.is-open { display: block; }
    @media (max-width: 760px) {
      .shell { width: min(100vw - 24px, 1240px); padding-top: 92px; }
      .top-tools { top: 12px; right: 12px; }
      .hero { margin-bottom: 34px; align-items: start; justify-items: stretch; }
      .title-row { justify-content: flex-start; align-items: flex-start; }
      h1 { font-size: 44px; }
      .clock { padding-bottom: 0; }
      .group-title { align-items: flex-start; }
      .group-actions { padding-top: 2px; }
      .search-wrap { border-radius: 14px; }
      .icon-grid { grid-template-columns: repeat(auto-fill, minmax(74px, 1fr)); gap: 22px 14px; }
      .icon-button { width: 66px; height: 66px; border-radius: 13px; }
      .icon-button iconify-icon { font-size: 36px; }
      .app-name { width: 78px; font-size: 13px; }
      .drag-placeholder { min-height: 109px; border-radius: 14px; }
      .menu { left: 12px !important; right: 12px; top: auto !important; bottom: 12px; width: auto; }
      .modal { border-radius: 18px; padding: 22px 18px 24px; }
      .confirm-modal { padding: 22px 18px; border-radius: 16px; }
      .confirm-actions { flex-direction: column-reverse; }
      .confirm-cancel, .confirm-delete { width: 100%; }
      .preview { grid-template-columns: 1fr; padding: 16px; }
      .preview-wide { justify-self: stretch; width: 100%; height: 110px; font-size: 22px; }
      .preview-square { width: 100%; }
      .form-grid { grid-template-columns: 1fr; }
      .field.full { grid-column: auto; }
      .field label { font-size: 17px; }
      .field input, .field textarea, .field select { min-height: 50px; font-size: 16px; }
      .color-row { grid-template-columns: 70px 1fr; }
      .form-actions { justify-content: stretch; }
      .save-button, .secondary-button { width: 100%; }
    }
  </style>
</head>
<body style="{{.BackgroundCSS}}" data-background-color="{{.Appearance.BackgroundColor}}" data-background-image="{{.Appearance.BackgroundImage}}">
	  <div class="top-tools">
	    <button class="tool-button" type="button" id="access-mode-button" title="当前使用外网入口"><iconify-icon id="access-mode-icon" icon="mdi:web"></iconify-icon></button>
	    <button class="tool-button sort-button" type="button" id="save-sort-button" title="保存排序" disabled><iconify-icon icon="mdi:content-save-outline"></iconify-icon></button>
	    <button class="tool-button" type="button" id="open-settings-button" title="页面设置"><iconify-icon icon="mdi:image-edit-outline"></iconify-icon></button>
    {{if .Auth.Enabled}}<form method="post" action="/logout"><button class="tool-button" type="submit" title="退出登录"><iconify-icon icon="mdi:logout"></iconify-icon></button></form>{{end}}
  </div>
  <main class="shell">
    <section class="hero">
      <div class="title-row">
        <h1>{{.Title}}</h1>
        <div class="clock"><div class="clock-time" id="clock-time">--:--:--</div><div class="clock-date" id="clock-date">--</div></div>
      </div>
      <div class="search-wrap"><iconify-icon icon="mdi:magnify"></iconify-icon><input id="search" class="search" type="search" placeholder="搜索服务、描述或标签" autocomplete="off"></div>
    </section>

    <section class="groups">
      {{range .Groups}}
      <section class="group" data-group-id="{{.ID}}">
        <div class="group-title">
	          <div class="group-heading"><h2>{{.Name}}</h2><span class="group-count"><span class="group-visible-count">0</span> / <span class="group-total-count">{{len .Services}}</span></span></div>
          <div class="group-actions">
            <button class="group-action" type="button" data-action="add-service" data-group-id="{{.ID}}" title="新增入口"><iconify-icon icon="mdi:plus"></iconify-icon></button>
            <button class="group-action edit-mode-button" type="button" data-action="toggle-edit-mode" title="编辑模式"><iconify-icon icon="mdi:cursor-default-click-outline"></iconify-icon></button>
          </div>
        </div>
        <div class="icon-grid">
          {{range .Services}}
          <div class="app-icon" data-service-id="{{.ID}}" data-group-id="{{.GroupID}}" data-name="{{.Name}}" data-description="{{.Description}}" data-icon-text="{{.IconText}}" data-icon-value="{{.Icon}}" data-internal-url="{{.InternalURL}}" data-external-url="{{.ExternalURL}}" data-tags="{{range $i, $tag := .Tags}}{{if $i}},{{end}}{{.}}{{end}}" data-notes="{{.Notes}}" data-health-type="{{.Health.Type}}" data-health-url="{{.Health.URL}}" data-health-address="{{.Health.Address}}" data-health-expect-status="{{.Health.ExpectStatus}}" data-health-timeout="{{.Health.Timeout}}" data-search="{{.Name}} {{.Description}} {{range .Tags}}{{.}} {{end}}">
            <a class="icon-button" href="{{.DefaultURL}}" target="_blank" rel="noreferrer" aria-label="{{.Name}}">
              {{if .IconIsOnline}}<img src="{{.IconImageSrc}}" alt="">{{else if .IconIsImage}}<img src="{{.Icon}}" alt="">{{else}}<span class="icon-fallback">{{.DisplayIconText}}</span>{{end}}
              <span class="health-dot" data-status="unknown"></span>
            </a>
            <div class="app-name">{{.Name}}</div>
          </div>
          {{end}}
        </div>
      </section>
      {{end}}
    </section>
    <p class="empty">没有匹配的服务入口。</p>
  </main>

  <div class="menu" id="item-menu" role="menu" aria-hidden="true">
    <div class="menu-section"><p class="menu-title">打开外网入口</p><div class="menu-actions"><button class="menu-icon" type="button" data-action="open-external"><iconify-icon icon="mdi:open-in-new"></iconify-icon></button><button class="menu-icon" type="button" data-action="copy-external"><iconify-icon icon="mdi:link-variant"></iconify-icon></button></div></div>
    <div class="menu-section"><p class="menu-title">打开内网入口</p><div class="menu-actions"><button class="menu-icon" type="button" data-action="open-internal"><iconify-icon icon="mdi:open-in-new"></iconify-icon></button><button class="menu-icon" type="button" data-action="copy-internal"><iconify-icon icon="mdi:link-variant"></iconify-icon></button></div></div>
    <div class="menu-line"></div>
    <button class="menu-command" type="button" data-action="edit"><iconify-icon icon="mdi:pencil-box-outline"></iconify-icon>编辑</button>
    <button class="menu-command" type="button" data-action="delete"><iconify-icon icon="mdi:trash-can-outline"></iconify-icon>删除</button>
  </div>

  <div class="modal-backdrop" id="edit-backdrop">
    <section class="modal" role="dialog" aria-modal="true" aria-labelledby="edit-title">
      <div class="modal-head"><h2 class="modal-title" id="edit-title">编辑入口</h2><button class="close-button" type="button" id="edit-close" aria-label="关闭"><iconify-icon icon="mdi:close"></iconify-icon></button></div>
      <div class="preview"><div class="preview-wide"><div class="preview-icon" id="preview-wide-icon"></div><span id="preview-wide-name">-</span></div><div class="preview-square"><div class="preview-icon" id="preview-square-icon"></div><span id="preview-square-name">-</span></div></div>
      <form id="edit-form">
        <input type="hidden" name="id">
        <div class="form-grid">
          <div class="field"><label>名称 *</label><input name="name" maxlength="80" required></div>
          <div class="field"><label>描述</label><input name="description" maxlength="140"></div>
          <div class="field"><label>图标文字</label><input name="icon_text" maxlength="12" placeholder="NAS"></div>
          <div class="field"><label>在线图标名或图片 URL <a class="field-link" href="https://icon-sets.iconify.design/" target="_blank" rel="noreferrer">在线图标库</a></label><div class="icon-field-row"><input name="icon" placeholder="mdi:nas"><button class="upload-button" type="button" id="upload-icon-button">上传图片</button></div><input class="file-input" id="upload-icon-file" type="file" accept="image/png,image/jpeg,image/webp,image/svg+xml,image/x-icon"></div>
          <div class="field full"><label>外网入口</label><input name="external_url" type="url" placeholder="https://example.com"></div>
          <div class="field full"><label>内网入口</label><input name="internal_url" type="url" placeholder="http://192.168.x.x:8080"></div>
          <div class="field"><label>分组</label><select name="group_id">{{range .Groups}}<option value="{{.ID}}">{{.Name}}</option>{{end}}</select></div>
          <div class="field"><label>标签</label><input name="tags" placeholder="docker, tools"></div>
          <div class="field full"><label>备注</label><textarea name="notes"></textarea></div>
          <div class="field"><label>健康检查类型</label><select name="health_type"><option value="disabled">disabled</option><option value="http">http</option><option value="tcp">tcp</option></select></div>
          <div class="field"><label>预期状态码</label><input name="health_expect_status" type="number" min="100" max="599" placeholder="200"></div>
          <div class="field full"><label>健康检查 URL</label><input name="health_url" placeholder="http://service/healthz"></div>
          <div class="field"><label>TCP 地址</label><input name="health_address" placeholder="host:port"></div>
          <div class="field"><label>超时时间</label><input name="health_timeout" placeholder="2s"></div>
        </div>
        <div class="form-actions"><button class="delete-button" type="button" id="delete-service-button">删除</button><button class="save-button" type="submit">保存</button></div>
      </form>
    </section>
  </div>
  <div class="modal-backdrop" id="settings-backdrop">
    <section class="modal" role="dialog" aria-modal="true" aria-labelledby="settings-title">
      <div class="modal-head"><h2 class="modal-title" id="settings-title">页面设置</h2><button class="close-button" type="button" id="settings-close" aria-label="关闭"><iconify-icon icon="mdi:close"></iconify-icon></button></div>
      <div class="settings-preview" id="settings-preview"><span>背景预览</span></div>
      <form id="settings-form">
        <div class="form-grid">
          <div class="field"><label>背景颜色</label><div class="color-row"><input name="background_color_picker" type="color"><input name="background_color" placeholder="#000000"></div></div>
          <div class="field"><label>背景图片</label><div class="icon-field-row"><input name="background_image" placeholder="/uploads/background.png 或 https://example.com/bg.jpg"><button class="upload-button" type="button" id="upload-background-button">上传图片</button></div><input class="file-input" id="upload-background-file" type="file" accept="image/png,image/jpeg,image/webp,image/svg+xml"></div>
        </div>
        <div class="form-actions"><button class="secondary-button" type="button" id="reset-background-button">恢复默认</button><button class="save-button" type="submit">保存</button></div>
      </form>
    </section>
  </div>
  <div class="modal-backdrop confirm-backdrop" id="delete-confirm-backdrop" aria-hidden="true">
    <section class="confirm-modal" role="dialog" aria-modal="true" aria-labelledby="delete-confirm-title" aria-describedby="delete-confirm-description">
      <div class="confirm-head"><div class="confirm-mark"><iconify-icon icon="mdi:trash-can-outline"></iconify-icon></div><h2 class="confirm-title" id="delete-confirm-title">删除导航入口</h2></div>
      <p class="confirm-body" id="delete-confirm-description">确定删除导航入口 <span class="confirm-name" id="delete-confirm-name">当前入口</span> 吗？</p>
      <p class="confirm-note">只会从导航配置里移除入口，不会删除、停止或重启真实服务。</p>
      <div class="confirm-actions"><button class="confirm-cancel" type="button" id="cancel-delete-button">取消</button><button class="confirm-delete" type="button" id="confirm-delete-button">删除</button></div>
    </section>
  </div>
  <div class="toast" id="toast"></div>

  <script>
    const searchInput = document.querySelector('#search');
    const items = [...document.querySelectorAll('.app-icon')];
    const groups = [...document.querySelectorAll('.group')];
    const menu = document.querySelector('#item-menu');
    const backdrop = document.querySelector('#edit-backdrop');
    const settingsBackdrop = document.querySelector('#settings-backdrop');
    const deleteConfirmBackdrop = document.querySelector('#delete-confirm-backdrop');
    const form = document.querySelector('#edit-form');
    const settingsForm = document.querySelector('#settings-form');
    const toast = document.querySelector('#toast');
    const uploadButton = document.querySelector('#upload-icon-button');
    const uploadFile = document.querySelector('#upload-icon-file');
    const uploadBackgroundButton = document.querySelector('#upload-background-button');
    const uploadBackgroundFile = document.querySelector('#upload-background-file');
    const settingsPreview = document.querySelector('#settings-preview');
    const editTitle = document.querySelector('#edit-title');
    const saveButton = form.querySelector('.save-button');
    const deleteButton = document.querySelector('#delete-service-button');
    const cancelDeleteButton = document.querySelector('#cancel-delete-button');
    const confirmDeleteButton = document.querySelector('#confirm-delete-button');
    const deleteConfirmName = document.querySelector('#delete-confirm-name');
    const saveSortButton = document.querySelector('#save-sort-button');
    const accessModeButton = document.querySelector('#access-mode-button');
    const accessModeIcon = document.querySelector('#access-mode-icon');
    const statusLabels = { healthy: '正常', unhealthy: '异常', unknown: '未知', disabled: '未启用' };
    const accessModeKey = 'home-nav.access-mode';
    let activeItem = null;
    let editMode = false;
    let accessMode = 'external';
    let sortDirty = false;
    let dragState = null;
    let suppressNextEditClick = false;
    let pendingDelete = null;

    function field(name) { return form.elements.namedItem(name); }
    function settingField(name) { return settingsForm.elements.namedItem(name); }
    function updateClock() {
      const now = new Date();
      document.querySelector('#clock-time').textContent = now.toLocaleTimeString('en-GB', { hour12: false });
      document.querySelector('#clock-date').textContent = now.toLocaleDateString('en-US', { month: 'numeric', day: 'numeric', weekday: 'long' });
    }
    function normalize(value) { return (value || '').trim().toLowerCase(); }
    function showToast(message) { toast.textContent = message; toast.classList.add('is-open'); setTimeout(() => toast.classList.remove('is-open'), 1800); }
    function itemURL(type) { return type === 'internal' ? activeItem?.dataset.internalUrl : activeItem?.dataset.externalUrl; }
    function preferredURL(item, mode) {
      const internalURL = item.dataset.internalUrl || '';
      const externalURL = item.dataset.externalUrl || '';
      if (mode === 'internal') return internalURL || externalURL;
      return externalURL || internalURL;
    }
    function savedAccessMode() {
      try {
        return localStorage.getItem(accessModeKey) === 'internal' ? 'internal' : 'external';
      } catch (_) {
        return 'external';
      }
    }
    function setAccessMode(mode, notify) {
      accessMode = mode === 'internal' ? 'internal' : 'external';
      document.body.dataset.accessMode = accessMode;
      accessModeButton.title = accessMode === 'internal' ? '当前使用内网入口' : '当前使用外网入口';
      accessModeIcon.setAttribute('icon', accessMode === 'internal' ? 'mdi:lan' : 'mdi:web');
      for (const item of items) {
        const link = item.querySelector('.icon-button');
        const url = preferredURL(item, accessMode);
        if (url) link.href = url;
        link.dataset.activeUrlType = accessMode;
      }
      try { localStorage.setItem(accessModeKey, accessMode); } catch (_) {}
      if (notify) showToast(accessMode === 'internal' ? '已切换到内网入口' : '已切换到外网入口');
    }
    function toggleAccessMode() { setAccessMode(accessMode === 'internal' ? 'external' : 'internal', true); }
    function onlineIconSrc(icon) {
      const parts = String(icon || '').split(':');
      if (parts.length !== 2 || !parts[0] || !parts[1]) return '';
      return '/.iconify/' + encodeURIComponent(parts[0]) + '/' + encodeURIComponent(parts[1]) + '.svg';
    }
    function escapeHTML(value) { return String(value || '').replace(/[&<>"]/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c])); }
    function iconMarkup(item) {
      const icon = item.dataset.iconValue || '';
      const text = item.dataset.iconText || (item.dataset.name || '?').slice(0, 3).toUpperCase();
      if (icon.startsWith('http://') || icon.startsWith('https://') || icon.startsWith('/')) return '<img src="' + escapeHTML(icon) + '" alt="">';
      if (icon.includes(':')) return '<img src="' + escapeHTML(onlineIconSrc(icon)) + '" alt="">';
      return '<span class="icon-fallback">' + escapeHTML(text) + '</span>';
    }
    function applyFilters() {
      const keyword = normalize(searchInput.value);
      let visibleTotal = 0;
      for (const item of items) {
        const visible = !keyword || normalize(item.dataset.search).includes(keyword);
        item.classList.toggle('is-hidden', !visible);
        if (visible) visibleTotal += 1;
      }
	      for (const group of groups) {
	        const totalCount = group.querySelectorAll('.app-icon').length;
	        const visibleCount = group.querySelectorAll('.app-icon:not(.is-hidden)').length;
	        group.classList.toggle('is-hidden', visibleCount === 0);
	        group.querySelector('.group-visible-count').textContent = String(visibleCount);
	        group.querySelector('.group-total-count').textContent = String(totalCount);
	      }
      document.body.classList.toggle('is-empty', visibleTotal === 0);
    }
    function openMenuAt(item, left, top) {
      activeItem = item;
      menu.classList.add('is-open');
      menu.setAttribute('aria-hidden', 'false');
      const menuLeft = Math.min(left, window.innerWidth - menu.offsetWidth - 12);
      const menuTop = Math.min(top, window.innerHeight - menu.offsetHeight - 12);
      menu.style.left = Math.max(12, menuLeft) + 'px';
      menu.style.top = Math.max(12, menuTop) + 'px';
    }
    function openMenuNear(item, anchor) {
      const rect = anchor.getBoundingClientRect();
      openMenuAt(item, rect.left, rect.bottom + 10);
    }
    function closeMenu() { menu.classList.remove('is-open'); menu.setAttribute('aria-hidden', 'true'); }
	    function setEditMode(value) {
	      editMode = value;
	      document.body.classList.toggle('is-edit-mode', editMode);
	      for (const button of document.querySelectorAll('.edit-mode-button')) button.classList.toggle('is-active', editMode);
	      saveSortButton.disabled = !sortDirty;
	      showToast(editMode ? '编辑模式已开启' : (sortDirty ? '编辑模式已关闭，排序未保存' : '编辑模式已关闭'));
	    }
	    function suppressEditClickOnce() {
	      suppressNextEditClick = true;
	      window.setTimeout(() => { suppressNextEditClick = false; }, 240);
	    }
	    function setSortDirty(value) {
	      sortDirty = value;
	      saveSortButton.disabled = !sortDirty;
	    }
    function resetServiceForm(groupID) {
      editTitle.textContent = '新增入口';
      saveButton.textContent = '新增';
      deleteButton.classList.remove('is-visible');
      field('id').value = '';
      field('name').value = '';
      field('description').value = '';
      field('icon_text').value = '';
      field('icon').value = '';
      field('external_url').value = '';
      field('internal_url').value = '';
      field('group_id').value = groupID || groups[0]?.dataset.groupId || '';
      field('tags').value = '';
      field('notes').value = '';
      field('health_type').value = 'disabled';
      field('health_url').value = '';
      field('health_address').value = '';
      field('health_expect_status').value = '';
      field('health_timeout').value = '2s';
    }
    function openCreate(groupID) {
      resetServiceForm(groupID);
      refreshPreview();
      backdrop.classList.add('is-open');
      closeMenu();
    }
    function openEdit(item) {
      activeItem = item;
      editTitle.textContent = '编辑入口';
      saveButton.textContent = '保存';
      deleteButton.classList.add('is-visible');
      field('id').value = item.dataset.serviceId;
      field('name').value = item.dataset.name || '';
      field('description').value = item.dataset.description || '';
      field('icon_text').value = item.dataset.iconText || '';
      field('icon').value = item.dataset.iconValue || '';
      field('external_url').value = item.dataset.externalUrl || '';
      field('internal_url').value = item.dataset.internalUrl || '';
      field('group_id').value = item.dataset.groupId || '';
      field('tags').value = item.dataset.tags || '';
      field('notes').value = item.dataset.notes || '';
      field('health_type').value = item.dataset.healthType || 'disabled';
      field('health_url').value = item.dataset.healthUrl || '';
      field('health_address').value = item.dataset.healthAddress || '';
      field('health_expect_status').value = item.dataset.healthExpectStatus === '0' ? '' : (item.dataset.healthExpectStatus || '');
      field('health_timeout').value = item.dataset.healthTimeout || '2s';
      refreshPreview();
      backdrop.classList.add('is-open');
      closeMenu();
    }
    function closeEdit() { backdrop.classList.remove('is-open'); }
    function refreshPreview() {
      const mock = { dataset: { iconValue: field('icon').value, iconText: field('icon_text').value, name: field('name').value } };
      document.querySelector('#preview-wide-icon').innerHTML = iconMarkup(mock);
      document.querySelector('#preview-square-icon').innerHTML = iconMarkup(mock);
      document.querySelector('#preview-wide-name').textContent = field('name').value || '-';
      document.querySelector('#preview-square-name').textContent = field('name').value || '-';
    }
    function normalizeHexColor(value) {
      return /^#[0-9a-fA-F]{6}$/.test(value || '') ? value : '#000000';
    }
    function backgroundStyle(color, image) {
      const style = { backgroundColor: color || '#000000', backgroundImage: '', backgroundSize: '', backgroundPosition: '', backgroundAttachment: '' };
      if (image) {
        style.backgroundImage = 'linear-gradient(rgba(0,0,0,.18),rgba(0,0,0,.18)),url("' + String(image).replace(/["\\\n\r]/g, '') + '")';
        style.backgroundSize = 'cover';
        style.backgroundPosition = 'center';
        style.backgroundAttachment = 'fixed';
      }
      return style;
    }
    function applyBackgroundPreview(color, image, target) {
      const style = backgroundStyle(color, image);
      target.style.backgroundColor = style.backgroundColor;
      target.style.backgroundImage = style.backgroundImage;
      target.style.backgroundSize = style.backgroundSize;
      target.style.backgroundPosition = style.backgroundPosition;
      target.style.backgroundAttachment = '';
    }
    function applyBodyBackground(color, image) {
      const style = backgroundStyle(color, image);
      document.body.style.backgroundColor = style.backgroundColor;
      document.body.style.backgroundImage = style.backgroundImage;
      document.body.style.backgroundSize = style.backgroundSize;
      document.body.style.backgroundPosition = style.backgroundPosition;
      document.body.style.backgroundAttachment = style.backgroundAttachment;
    }
    function openSettings() {
      const color = document.body.dataset.backgroundColor || '#000000';
      const image = document.body.dataset.backgroundImage || '';
      settingField('background_color').value = color;
      settingField('background_color_picker').value = normalizeHexColor(color);
      settingField('background_image').value = image;
      applyBackgroundPreview(color, image, settingsPreview);
      settingsBackdrop.classList.add('is-open');
    }
    function closeSettings() { settingsBackdrop.classList.remove('is-open'); }
    async function copyText(value) {
      if (!value) return showToast('没有可复制的链接');
      await navigator.clipboard.writeText(value);
      showToast('链接已复制');
    }
    async function refreshStatus() {
      try {
        const response = await fetch('/api/status', { cache: 'no-store' });
        if (!response.ok) return;
        const payload = await response.json();
        for (const item of items) {
          const status = payload.services?.[item.dataset.serviceId];
          if (!status) continue;
          const dot = item.querySelector('.health-dot');
          dot.dataset.status = status.status || 'unknown';
          dot.title = status.error || (statusLabels[status.status] || '未知');
        }
      } catch (_) {}
    }
    async function saveItem(event) {
      event.preventDefault();
      const id = field('id').value;
      const expectStatus = Number(field('health_expect_status').value || 0);
      const payload = {
        name: field('name').value,
        description: field('description').value,
        icon_text: field('icon_text').value,
        icon: field('icon').value,
        external_url: field('external_url').value,
        internal_url: field('internal_url').value,
        group_id: field('group_id').value,
        tags: field('tags').value.split(',').map(v => v.trim()).filter(Boolean),
        notes: field('notes').value,
        health: {
          type: field('health_type').value,
          url: field('health_url').value,
          address: field('health_address').value,
          expect_status: expectStatus,
          timeout: field('health_timeout').value || '2s'
        }
      };
      const url = id ? '/api/services/' + encodeURIComponent(id) : '/api/services';
      const method = id ? 'PUT' : 'POST';
      const response = await fetch(url, { method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload) });
      if (!response.ok) {
        const error = await response.json().catch(() => ({ error: '保存失败' }));
        showToast(error.error || '保存失败');
        return;
      }
      showToast(id ? '已保存' : '已新增');
      setTimeout(() => location.reload(), 500);
    }
    function openDeleteConfirm(id, name) {
      if (!id) return;
      pendingDelete = { id, name: name || '当前入口' };
      deleteConfirmName.textContent = '“' + pendingDelete.name + '”';
      confirmDeleteButton.disabled = false;
      confirmDeleteButton.textContent = '删除';
      deleteConfirmBackdrop.classList.add('is-open');
      deleteConfirmBackdrop.setAttribute('aria-hidden', 'false');
      confirmDeleteButton.focus();
    }
    function closeDeleteConfirm() {
      pendingDelete = null;
      deleteConfirmBackdrop.classList.remove('is-open');
      deleteConfirmBackdrop.setAttribute('aria-hidden', 'true');
      confirmDeleteButton.disabled = false;
      confirmDeleteButton.textContent = '删除';
    }
    async function performDelete() {
      if (!pendingDelete?.id) return;
      const id = pendingDelete.id;
      confirmDeleteButton.disabled = true;
      confirmDeleteButton.textContent = '删除中';
      const response = await fetch('/api/services/' + encodeURIComponent(id), { method: 'DELETE' });
      if (!response.ok) {
        const error = await response.json().catch(() => ({ error: '删除失败' }));
        showToast(error.error || '删除失败');
        confirmDeleteButton.disabled = false;
        confirmDeleteButton.textContent = '删除';
        return;
      }
      showToast('已删除');
      setTimeout(() => location.reload(), 500);
    }
    async function deleteItem() {
      openDeleteConfirm(field('id').value, field('name').value);
    }
    async function saveSettings(event) {
      event.preventDefault();
      const payload = {
        background_color: settingField('background_color').value,
        background_image: settingField('background_image').value
      };
      const response = await fetch('/api/settings', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload) });
      if (!response.ok) {
        const error = await response.json().catch(() => ({ error: '保存失败' }));
        showToast(error.error || '保存失败');
        return;
      }
      document.body.dataset.backgroundColor = payload.background_color;
      document.body.dataset.backgroundImage = payload.background_image;
      applyBodyBackground(payload.background_color, payload.background_image);
      showToast('页面设置已保存');
	      closeSettings();
	    }
	    function sortPayload() {
	      return {
	        groups: groups.map(group => ({
	          group_id: group.dataset.groupId,
	          service_ids: [...group.querySelectorAll('.app-icon')].map(item => item.dataset.serviceId)
	        }))
	      };
	    }
	    async function saveSort() {
	      if (!sortDirty) return;
	      saveSortButton.disabled = true;
	      const response = await fetch('/api/services/sort', {
	        method: 'PUT',
	        headers: { 'Content-Type': 'application/json' },
	        body: JSON.stringify(sortPayload())
	      });
	      if (!response.ok) {
	        const error = await response.json().catch(() => ({ error: '保存排序失败' }));
	        showToast(error.error || '保存排序失败');
	        saveSortButton.disabled = false;
	        return;
	      }
	      setSortDirty(false);
	      showToast('排序已保存');
	      setTimeout(() => location.reload(), 500);
	    }
    async function uploadIcon(file) {
      if (!file) return;
      const formData = new FormData();
      formData.append('file', file);
      uploadButton.disabled = true;
      uploadButton.textContent = '上传中';
      try {
        const response = await fetch('/api/uploads', { method: 'POST', body: formData });
        const payload = await response.json().catch(() => ({}));
        if (!response.ok) {
          showToast(payload.error || '上传失败');
          return;
        }
        field('icon').value = payload.url || '';
        refreshPreview();
        showToast('图片已上传');
      } finally {
        uploadButton.disabled = false;
        uploadButton.textContent = '上传图片';
        uploadFile.value = '';
      }
    }
	    async function uploadBackground(file) {
      if (!file) return;
      const formData = new FormData();
      formData.append('file', file);
      uploadBackgroundButton.disabled = true;
      uploadBackgroundButton.textContent = '上传中';
      try {
        const response = await fetch('/api/uploads', { method: 'POST', body: formData });
        const payload = await response.json().catch(() => ({}));
        if (!response.ok) {
          showToast(payload.error || '上传失败');
          return;
        }
        settingField('background_image').value = payload.url || '';
        applyBackgroundPreview(settingField('background_color').value, settingField('background_image').value, settingsPreview);
        showToast('背景图已上传');
      } finally {
        uploadBackgroundButton.disabled = false;
        uploadBackgroundButton.textContent = '上传图片';
        uploadBackgroundFile.value = '';
      }
	    }
	    function dragPlaceholderFor(item) {
	      const placeholder = document.createElement('div');
	      placeholder.className = 'drag-placeholder';
	      placeholder.setAttribute('aria-hidden', 'true');
	      const rect = item.getBoundingClientRect();
	      placeholder.style.minHeight = Math.round(rect.height) + 'px';
	      return placeholder;
	    }
	    function positionDraggedItem(state) {
	      state.frame = 0;
	      if (!state.dragging) return;
	      const x = state.lastX - state.offsetX;
	      const y = state.lastY - state.offsetY;
	      state.item.style.transform = 'translate3d(' + Math.round(x) + 'px,' + Math.round(y) + 'px,0)';
	      placeDraggedItem(state.lastX, state.lastY);
	      autoScrollWhileDragging(state.lastY);
	    }
	    function scheduleDragFrame(state) {
	      if (state.frame) return;
	      state.frame = window.requestAnimationFrame(() => positionDraggedItem(state));
	    }
	    function autoScrollWhileDragging(clientY) {
	      const edge = 76;
	      const maxStep = 18;
	      let step = 0;
	      if (clientY < edge) step = -Math.ceil((edge - clientY) / edge * maxStep);
	      if (clientY > window.innerHeight - edge) step = Math.ceil((clientY - (window.innerHeight - edge)) / edge * maxStep);
	      if (step) window.scrollBy({ top: step, behavior: 'auto' });
	    }
	    function animatedSortNodes() {
	      return [...document.querySelectorAll('.app-icon:not(.is-dragging), .drag-placeholder')];
	    }
	    function captureSortRects() {
	      const rects = new Map();
	      for (const node of animatedSortNodes()) {
	        if (node.getClientRects().length) rects.set(node, node.getBoundingClientRect());
	      }
	      return rects;
	    }
	    function animateGridMove(mutator) {
	      const before = captureSortRects();
	      mutator();
	      const moved = [];
	      for (const node of animatedSortNodes()) {
	        const first = before.get(node);
	        if (!first) continue;
	        const last = node.getBoundingClientRect();
	        const dx = first.left - last.left;
	        const dy = first.top - last.top;
	        if (Math.abs(dx) < 1 && Math.abs(dy) < 1) continue;
	        node.style.transition = 'none';
	        node.style.transform = 'translate3d(' + Math.round(dx) + 'px,' + Math.round(dy) + 'px,0)';
	        moved.push(node);
	      }
	      if (!moved.length) return;
	      for (const node of moved) node.getBoundingClientRect();
	      window.requestAnimationFrame(() => {
	        for (const node of moved) {
	          node.style.transition = 'transform 150ms cubic-bezier(.2,0,.2,1)';
	          node.style.transform = 'translate3d(0,0,0)';
	          window.setTimeout(() => {
	            if (node.classList.contains('is-dragging')) return;
	            node.style.transition = '';
	            node.style.transform = '';
	          }, 180);
	        }
	      });
	    }
	    function movePlaceholderBefore(targetItem) {
	      if (dragState.placeholder.nextElementSibling === targetItem) return;
	      animateGridMove(() => targetItem.before(dragState.placeholder));
	    }
	    function movePlaceholderAfter(targetItem) {
	      if (dragState.placeholder.previousElementSibling === targetItem) return;
	      animateGridMove(() => targetItem.after(dragState.placeholder));
	    }
	    function appendPlaceholderTo(targetGrid) {
	      if (dragState.placeholder.parentElement === targetGrid && !dragState.placeholder.nextElementSibling) return;
	      animateGridMove(() => targetGrid.append(dragState.placeholder));
	    }
	    function beginDrag(state) {
	      if (normalize(searchInput.value)) {
	        suppressEditClickOnce();
	        showToast('清空搜索后再排序');
	        return false;
	      }
	      const rect = state.item.getBoundingClientRect();
	      state.offsetX = state.startX - rect.left;
	      state.offsetY = state.startY - rect.top;
	      state.placeholder = dragPlaceholderFor(state.item);
	      state.item.after(state.placeholder);
	      state.dragging = true;
	      state.item.classList.add('is-dragging');
	      state.item.style.width = Math.round(rect.width) + 'px';
	      state.item.style.height = Math.round(rect.height) + 'px';
	      document.body.classList.add('is-dragging');
	      closeMenu();
	      positionDraggedItem(state);
	      return true;
	    }
	    function finishDrag() {
	      if (!dragState) return;
	      const state = dragState;
	      if (state.dragging && Number.isFinite(state.lastX) && Number.isFinite(state.lastY)) {
	        placeDraggedItem(state.lastX, state.lastY);
	      }
	      dragState = null;
	      if (state.frame) window.cancelAnimationFrame(state.frame);
	      if (state.placeholder?.isConnected) state.placeholder.replaceWith(state.item);
	      state.item.classList.remove('is-dragging');
	      state.item.style.width = '';
	      state.item.style.height = '';
	      state.item.style.transform = '';
	      document.body.classList.remove('is-dragging');
	      try { state.button.releasePointerCapture(state.pointerId); } catch (_) {}
	      if (state.dragging) {
	        const group = state.item.closest('.group');
	        if (group) state.item.dataset.groupId = group.dataset.groupId || '';
	        suppressEditClickOnce();
	        if (JSON.stringify(sortPayload()) !== state.startOrder) setSortDirty(true);
	        applyFilters();
	      }
	    }
	    function placeDraggedItem(clientX, clientY) {
	      if (!dragState?.dragging) return;
	      const target = document.elementFromPoint(clientX, clientY);
	      if (!target) return;
	      const targetItem = target.closest('.app-icon:not(.is-dragging)');
	      if (targetItem) {
	        const rect = targetItem.getBoundingClientRect();
	        const centerX = rect.left + rect.width / 2;
	        const upperBand = rect.top + rect.height * .35;
	        const lowerBand = rect.top + rect.height * .65;
	        const before = clientY < upperBand || (clientY <= lowerBand && clientX < centerX);
	        if (before) movePlaceholderBefore(targetItem);
	        else movePlaceholderAfter(targetItem);
	        return;
	      }
	      const targetGrid = target.closest('.icon-grid');
	      if (targetGrid) appendPlaceholderTo(targetGrid);
	    }
	    function startDragPointer(event, item, button) {
	      if (!editMode || event.button !== 0) return;
	      dragState = { item, button, pointerId: event.pointerId, startX: event.clientX, startY: event.clientY, lastX: event.clientX, lastY: event.clientY, offsetX: 0, offsetY: 0, startOrder: JSON.stringify(sortPayload()), dragging: false, placeholder: null, frame: 0 };
	      button.setPointerCapture(event.pointerId);
	    }
	    function moveDragPointer(event) {
	      if (!dragState || dragState.pointerId !== event.pointerId) return;
	      dragState.lastX = event.clientX;
	      dragState.lastY = event.clientY;
	      const distance = Math.hypot(event.clientX - dragState.startX, event.clientY - dragState.startY);
	      if (!dragState.dragging && distance > 8 && !beginDrag(dragState)) {
	        dragState = null;
	        return;
	      }
	      if (dragState?.dragging) {
	        event.preventDefault();
	        scheduleDragFrame(dragState);
	      }
	    }
	    function endDragPointer(event) {
	      if (dragState?.pointerId === event.pointerId) finishDrag();
	    }
	    function startMouseDrag(event, item, button) {
	      if (!editMode || event.button !== 0 || dragState) return;
	      dragState = { item, button, pointerId: 'mouse', startX: event.clientX, startY: event.clientY, lastX: event.clientX, lastY: event.clientY, offsetX: 0, offsetY: 0, startOrder: JSON.stringify(sortPayload()), dragging: false, placeholder: null, frame: 0 };
	    }
	    function moveMouseDrag(event) {
	      if (!dragState || dragState.pointerId !== 'mouse') return;
	      dragState.lastX = event.clientX;
	      dragState.lastY = event.clientY;
	      const distance = Math.hypot(event.clientX - dragState.startX, event.clientY - dragState.startY);
	      if (!dragState.dragging && distance > 8 && !beginDrag(dragState)) {
	        dragState = null;
	        return;
	      }
	      if (dragState?.dragging) {
	        event.preventDefault();
	        scheduleDragFrame(dragState);
	      }
	    }
	    function endMouseDrag() {
	      if (dragState?.pointerId === 'mouse') finishDrag();
	    }

	    for (const item of items) {
	      const button = item.querySelector('.icon-button');
	      let longPressTimer = null;
	      let suppressClick = false;
	      button.addEventListener('dragstart', event => event.preventDefault());
	      button.addEventListener('click', event => {
	        if (suppressNextEditClick) {
	          event.preventDefault();
	          return;
	        }
	        if (editMode) {
	          event.preventDefault();
          openEdit(item);
          return;
        }
        if (suppressClick) {
          suppressClick = false;
          event.preventDefault();
        }
      });
      button.addEventListener('contextmenu', event => {
        event.preventDefault();
        openMenuAt(item, event.clientX, event.clientY);
      });
	      button.addEventListener('pointerdown', event => {
	        if (editMode) {
	          startDragPointer(event, item, button);
	          return;
	        }
	        if (event.pointerType === 'mouse') return;
	        window.clearTimeout(longPressTimer);
        longPressTimer = window.setTimeout(() => {
          suppressClick = true;
          openMenuNear(item, button);
        }, 520);
	      });
	      button.addEventListener('pointermove', moveDragPointer);
	      button.addEventListener('mousedown', event => startMouseDrag(event, item, button));
	      for (const eventName of ['pointerup', 'pointercancel']) {
	        button.addEventListener(eventName, event => {
	          window.clearTimeout(longPressTimer);
	          endDragPointer(event);
	        });
	      }
	      button.addEventListener('pointerleave', () => {
	        if (!dragState) window.clearTimeout(longPressTimer);
	      });
	    }
	    document.addEventListener('click', event => {
	      const actionButton = event.target.closest('.group-action[data-action]');
	      if (!actionButton) return;
	      if (actionButton.dataset.action === 'add-service') openCreate(actionButton.dataset.groupId || '');
	      if (actionButton.dataset.action === 'toggle-edit-mode') setEditMode(!editMode);
	    });
	    document.addEventListener('pointerup', endDragPointer);
	    document.addEventListener('pointercancel', endDragPointer);
	    document.addEventListener('mousemove', moveMouseDrag);
	    document.addEventListener('mouseup', endMouseDrag);
    menu.addEventListener('click', event => {
      const button = event.target.closest('button[data-action]');
      if (!button || !activeItem) return;
      const action = button.dataset.action;
      if (action === 'open-external') { const url = itemURL('external'); url ? window.open(url, '_blank', 'noreferrer') : showToast('没有外网入口'); }
      if (action === 'open-internal') { const url = itemURL('internal'); url ? window.open(url, '_blank', 'noreferrer') : showToast('没有内网入口'); }
      if (action === 'copy-external') copyText(itemURL('external'));
      if (action === 'copy-internal') copyText(itemURL('internal'));
      if (action === 'edit') openEdit(activeItem);
      if (action === 'delete') {
        closeMenu();
        openDeleteConfirm(activeItem.dataset.serviceId, activeItem.dataset.name);
      }
    });
	    document.addEventListener('click', event => { if (!menu.contains(event.target) && !event.target.closest('.icon-button')) closeMenu(); });
	    accessModeButton.addEventListener('click', toggleAccessMode);
	    saveSortButton.addEventListener('click', saveSort);
    document.querySelector('#open-settings-button').addEventListener('click', openSettings);
    document.querySelector('#edit-close').addEventListener('click', closeEdit);
    document.querySelector('#settings-close').addEventListener('click', closeSettings);
    backdrop.addEventListener('click', event => { if (event.target === backdrop) closeEdit(); });
    settingsBackdrop.addEventListener('click', event => { if (event.target === settingsBackdrop) closeSettings(); });
    deleteConfirmBackdrop.addEventListener('click', event => { if (event.target === deleteConfirmBackdrop) closeDeleteConfirm(); });
    cancelDeleteButton.addEventListener('click', closeDeleteConfirm);
    confirmDeleteButton.addEventListener('click', performDelete);
    document.addEventListener('keydown', event => { if (event.key === 'Escape' && deleteConfirmBackdrop.classList.contains('is-open')) closeDeleteConfirm(); });
    form.addEventListener('input', refreshPreview);
    form.addEventListener('submit', saveItem);
    deleteButton.addEventListener('click', deleteItem);
    settingsForm.addEventListener('submit', saveSettings);
    settingsForm.addEventListener('input', () => applyBackgroundPreview(settingField('background_color').value, settingField('background_image').value, settingsPreview));
    settingField('background_color_picker').addEventListener('input', () => {
      settingField('background_color').value = settingField('background_color_picker').value;
      applyBackgroundPreview(settingField('background_color').value, settingField('background_image').value, settingsPreview);
    });
    settingField('background_color').addEventListener('input', () => {
      const value = settingField('background_color').value;
      if (/^#[0-9a-fA-F]{6}$/.test(value)) settingField('background_color_picker').value = value;
    });
    document.querySelector('#reset-background-button').addEventListener('click', () => {
      settingField('background_color').value = '#000000';
      settingField('background_color_picker').value = '#000000';
      settingField('background_image').value = '';
      applyBackgroundPreview('#000000', '', settingsPreview);
    });
    uploadButton.addEventListener('click', () => uploadFile.click());
    uploadFile.addEventListener('change', () => uploadIcon(uploadFile.files?.[0]));
    uploadBackgroundButton.addEventListener('click', () => uploadBackgroundFile.click());
    uploadBackgroundFile.addEventListener('change', () => uploadBackground(uploadBackgroundFile.files?.[0]));
    searchInput.addEventListener('input', applyFilters);
    updateClock(); setInterval(updateClock, 1000);
    setAccessMode(savedAccessMode(), false);
    applyFilters(); refreshStatus(); setInterval(refreshStatus, 30000);
  </script>
</body>
</html>`

const faviconSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">
  <rect width="64" height="64" rx="14" fill="#141414"/>
  <path d="M17 20h30v24H17z" fill="none" stroke="#f7f7f7" stroke-width="5" stroke-linejoin="round"/>
  <path d="M24 27h4v10h-4zm8 0h4v10h-4zm8 0h4v10h-4z" fill="#67e0b6"/>
</svg>`

const loginTemplate = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>登录 - {{.Title}}</title>
  <link rel="icon" href="/favicon.svg" type="image/svg+xml">
  <script src="https://code.iconify.design/iconify-icon/2.1.0/iconify-icon.min.js" defer></script>
  <style>
    :root {
      color-scheme: dark;
      --bg: #000;
      --surface: #151515;
      --field: #24242a;
      --text: #f7f7f7;
      --muted: #b5bac5;
      --line: #4c4d56;
      --accent: #67e0b6;
      --accent-strong: #8ff0ce;
      --bad: #ff8c8c;
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", sans-serif;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      min-height: 100vh;
      display: grid;
      place-items: center;
      background: var(--bg);
      color: var(--text);
      padding: 24px;
    }
    .top-tools { position: fixed; top: 22px; right: 22px; display: flex; gap: 10px; z-index: 20; }
    .tool-button { width: 48px; height: 48px; border: 0; border-radius: 8px; background: #141414; color: #fff; display: grid; place-items: center; cursor: pointer; }
    .tool-button:hover { background: #242424; }
    .tool-button iconify-icon { font-size: 22px; }
    main {
      width: min(430px, 100%);
      display: grid;
      gap: 22px;
    }
    h1 {
      margin: 0;
      font-size: clamp(40px, 10vw, 58px);
      line-height: .98;
      letter-spacing: 0;
      font-weight: 800;
      text-align: center;
    }
    .subtitle {
      margin: 12px 0 0;
      color: var(--muted);
      line-height: 1.6;
      text-align: center;
    }
    form {
      display: grid;
      gap: 16px;
      padding: 24px;
      border: 1px solid var(--line);
      border-radius: 14px;
      background: var(--surface);
    }
    label {
      display: grid;
      gap: 7px;
      color: var(--muted);
      font-size: 14px;
    }
    input {
      width: 100%;
      min-height: 48px;
      border: 1px solid transparent;
      border-radius: 8px;
      padding: 0 14px;
      color: var(--text);
      background: var(--field);
      font-size: 15px;
      outline: none;
    }
    input:focus {
      border-color: var(--accent);
      box-shadow: 0 0 0 3px rgba(103, 224, 182, .15);
    }
    button {
      min-height: 48px;
      border: 1px solid var(--accent);
      border-radius: 8px;
      background: var(--accent);
      color: #050505;
      font-size: 16px;
      cursor: pointer;
      font-weight: 700;
    }
    button:hover { background: var(--accent-strong); }
    .error {
      margin: 0;
      color: var(--bad);
      font-size: 14px;
      line-height: 1.5;
    }
    @media (max-width: 760px) {
      body { align-items: start; padding: 96px 18px 24px; }
      .top-tools { top: 12px; right: 12px; }
      form { padding: 20px; }
    }
  </style>
</head>
<body>
  <div class="top-tools">
    <button class="tool-button" type="button" id="access-mode-button" title="当前使用外网入口"><iconify-icon id="access-mode-icon" icon="mdi:web"></iconify-icon></button>
  </div>
  <main>
    <div>
      <h1>{{.Title}}</h1>
      <p class="subtitle">请登录后查看个人服务导航。</p>
    </div>
    <form method="post" action="/login?return_to={{.ReturnTo}}">
      {{if .Error}}<p class="error">{{.Error}}</p>{{end}}
      <label>账号
        <input name="username" type="text" autocomplete="username" autofocus required>
      </label>
      <label>密码
        <input name="password" type="password" autocomplete="current-password" required>
      </label>
      <button type="submit">登录</button>
    </form>
  </main>
  <script>
    const accessModeKey = 'home-nav.access-mode';
    const accessModeButton = document.querySelector('#access-mode-button');
    const accessModeIcon = document.querySelector('#access-mode-icon');
    function savedAccessMode() {
      try {
        return localStorage.getItem(accessModeKey) === 'internal' ? 'internal' : 'external';
      } catch (_) {
        return 'external';
      }
    }
    function setAccessMode(mode) {
      const accessMode = mode === 'internal' ? 'internal' : 'external';
      document.body.dataset.accessMode = accessMode;
      accessModeButton.title = accessMode === 'internal' ? '当前使用内网入口' : '当前使用外网入口';
      accessModeIcon.setAttribute('icon', accessMode === 'internal' ? 'mdi:lan' : 'mdi:web');
      try { localStorage.setItem(accessModeKey, accessMode); } catch (_) {}
    }
    accessModeButton.addEventListener('click', () => setAccessMode(document.body.dataset.accessMode === 'internal' ? 'external' : 'internal'));
    setAccessMode(savedAccessMode());
  </script>
</body>
</html>`
