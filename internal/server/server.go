package server

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
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
	setupTpl   *template.Template
	mux        *http.ServeMux
	iconMu     sync.RWMutex
	iconHTML   map[string]template.HTML
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

type SetupData struct {
	Title   string
	Error   string
	Allowed bool
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

type GroupUpdateRequest struct {
	Name string `json:"name"`
}

type GroupSortRequest struct {
	GroupIDs []string `json:"group_ids"`
}

type ServiceSortRequest struct {
	Groups []ServiceSortGroupRequest `json:"groups"`
}

type ServiceSortGroupRequest struct {
	GroupID    string   `json:"group_id"`
	ServiceIDs []string `json:"service_ids"`
}

type AppearanceUpdateRequest struct {
	BackgroundColor   string `json:"background_color"`
	BackgroundImage   string `json:"background_image"`
	BackgroundOverlay string `json:"background_overlay"`
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

	s := &Server{
		configPath: configPath,
		cfg:        cfg,
		statuses:   NewStatusCache(cfg),
		mux:        http.NewServeMux(),
		iconHTML:   make(map[string]template.HTML),
	}

	funcs := template.FuncMap{
		"icon":        s.inlineIconHTML,
		"iconJSON":    s.inlineIconJSON,
		"openHref":    openHref,
		"serviceIcon": s.serviceIconHTML,
	}
	indexTpl, err := template.New("index").Funcs(funcs).Parse(indexTemplate)
	if err != nil {
		return nil, err
	}
	loginTpl, err := template.New("login").Funcs(funcs).Parse(loginTemplate)
	if err != nil {
		return nil, err
	}
	setupTpl, err := template.New("setup").Funcs(funcs).Parse(setupTemplate)
	if err != nil {
		return nil, err
	}
	s.indexTpl = indexTpl
	s.loginTpl = loginTpl
	s.setupTpl = setupTpl

	s.routes()
	s.statuses.Start(context.Background(), cfg.CheckInterval)
	go s.prewarmIconCache(cfg)
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	s.mux.HandleFunc("/healthz", s.handleHealthz)
	s.mux.HandleFunc("/favicon.svg", handleFavicon)
	s.mux.HandleFunc("/favicon.ico", handleFavicon)
	s.mux.HandleFunc("/setup", s.handleSetup)
	s.mux.HandleFunc("/login", s.handleLogin)
	s.mux.HandleFunc("/logout", s.handleLogout)
	s.mux.HandleFunc("/open", s.handleOpen)
	s.mux.HandleFunc("/api/status", s.handleStatus)
	s.mux.HandleFunc("/api/groups/sort", s.handleGroupSort)
	s.mux.HandleFunc("/api/groups", s.handleGroups)
	s.mux.HandleFunc("/api/groups/", s.handleGroup)
	s.mux.HandleFunc("/api/services/sort", s.handleServiceSort)
	s.mux.HandleFunc("/api/services", s.handleServices)
	s.mux.HandleFunc("/api/services/", s.handleService)
	s.mux.HandleFunc("/api/settings", s.handleSettings)
	s.mux.HandleFunc("/api/assets", s.handleAssets)
	s.mux.HandleFunc("/api/uploads", s.handleUpload)
	s.mux.HandleFunc("/.iconify/", s.handleIconifyIcon)
	if s.cfg.Assets.UploadsDir != "" {
		uploads := http.StripPrefix(s.cfg.Assets.UploadsURLPrefix, http.FileServer(http.Dir(s.cfg.Assets.UploadsDir)))
		s.mux.Handle(s.cfg.Assets.UploadsURLPrefix, cacheStatic(uploads))
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

func (s *Server) handleOpen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.setupRequired() {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	if !s.authenticated(r) {
		redirectToLogin(w, r)
		return
	}

	target := strings.TrimSpace(r.URL.Query().Get("url"))
	if err := validateWebURL("url", target); err != nil {
		http.Error(w, "invalid url", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Referrer-Policy", "no-referrer")
	_ = openRedirectTpl.Execute(w, struct{ Target string }{Target: target})
}

func cacheStatic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, max-age=604800")
		next.ServeHTTP(w, r)
	})
}

func openHref(target string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return "#"
	}
	return target
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

func (s *Server) handleGroups(w http.ResponseWriter, r *http.Request) {
	if !s.authenticated(r) {
		writeJSONError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "请求方法不支持")
		return
	}

	var payload GroupUpdateRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&payload); err != nil {
		writeJSONError(w, http.StatusBadRequest, "请求内容无效")
		return
	}

	cfg, group, err := s.createGroup(payload)
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
	_ = json.NewEncoder(w).Encode(map[string]any{"group": group})
}

func (s *Server) handleGroupSort(w http.ResponseWriter, r *http.Request) {
	if !s.authenticated(r) {
		writeJSONError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	if r.Method != http.MethodPut {
		writeJSONError(w, http.StatusMethodNotAllowed, "请求方法不支持")
		return
	}

	var payload GroupSortRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&payload); err != nil {
		writeJSONError(w, http.StatusBadRequest, "请求内容无效")
		return
	}

	cfg, err := s.sortGroups(payload)
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

func (s *Server) handleGroup(w http.ResponseWriter, r *http.Request) {
	if !s.authenticated(r) {
		writeJSONError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	if r.Method != http.MethodPut && r.Method != http.MethodDelete {
		writeJSONError(w, http.StatusMethodNotAllowed, "请求方法不支持")
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/api/groups/")
	if id == "" || strings.Contains(id, "/") {
		writeJSONError(w, http.StatusNotFound, "分组不存在")
		return
	}

	var cfg *Config
	var group Group
	var err error
	if r.Method == http.MethodDelete {
		cfg, err = s.deleteGroup(id)
	} else {
		var payload GroupUpdateRequest
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&payload); err != nil {
			writeJSONError(w, http.StatusBadRequest, "请求内容无效")
			return
		}
		cfg, group, err = s.updateGroup(id, payload)
	}
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
	if r.Method == http.MethodDelete {
		_ = json.NewEncoder(w).Encode(map[string]any{"deleted": id})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"group": group})
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
	if s.setupRequired() {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
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

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if !s.setupRequired() {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	allowed := setupAllowedFromRequest(r)
	cfg := s.currentConfig()
	switch r.Method {
	case http.MethodGet:
		s.renderSetup(w, SetupData{Title: cfg.Title, Allowed: allowed})
	case http.MethodPost:
		if !allowed {
			s.renderSetup(w, SetupData{Title: cfg.Title, Error: "当前访问来源不是局域网或本机，不能执行首次设置。请先通过局域网地址访问，或在配置文件里手动设置管理员密码。", Allowed: false})
			return
		}
		if err := r.ParseForm(); err != nil {
			s.renderSetup(w, SetupData{Title: cfg.Title, Error: "设置请求无效", Allowed: allowed})
			return
		}
		username := strings.TrimSpace(r.FormValue("username"))
		password := r.FormValue("password")
		confirmPassword := r.FormValue("confirm_password")
		nextCfg, err := s.initializeAuth(username, password, confirmPassword)
		if err != nil {
			s.renderSetup(w, SetupData{Title: cfg.Title, Error: err.Error(), Allowed: allowed})
			return
		}
		if err := SaveConfig(s.configPath, nextCfg); err != nil {
			s.renderSetup(w, SetupData{Title: cfg.Title, Error: err.Error(), Allowed: allowed})
			return
		}
		s.mu.Lock()
		s.cfg = nextCfg
		s.mu.Unlock()
		s.setSessionCookie(w, r, nextCfg.Auth.Username)
		http.Redirect(w, r, "/", http.StatusSeeOther)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.setupRequired() {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
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

func (s *Server) renderSetup(w http.ResponseWriter, data SetupData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.setupTpl.Execute(w, data)
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

func setupAllowedFromRequest(r *http.Request) bool {
	ip := setupClientIP(r)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

func setupClientIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	remoteIP := net.ParseIP(strings.TrimSpace(host))
	if remoteIP == nil {
		return nil
	}
	if remoteIP.IsLoopback() || remoteIP.IsPrivate() {
		if forwarded := firstForwardedIP(r); forwarded != nil {
			return forwarded
		}
	}
	return remoteIP
}

func firstForwardedIP(r *http.Request) net.IP {
	for _, raw := range []string{r.Header.Get("X-Forwarded-For"), r.Header.Get("X-Real-IP")} {
		if raw == "" {
			continue
		}
		first, _, _ := strings.Cut(raw, ",")
		if ip := net.ParseIP(strings.TrimSpace(first)); ip != nil {
			return ip
		}
	}
	return nil
}

func (s *Server) createGroup(payload GroupUpdateRequest) (*Config, Group, error) {
	s.mu.RLock()
	cfg := cloneConfig(s.cfg)
	s.mu.RUnlock()

	group := Group{
		ID:   uniqueGroupID(cfg, payload.Name),
		Name: payload.Name,
	}
	cfg.Groups = append(cfg.Groups, group)
	if err := cfg.NormalizeAndValidate(); err != nil {
		return nil, Group{}, err
	}
	for _, candidate := range cfg.Groups {
		if candidate.ID == group.ID {
			return cfg, candidate, nil
		}
	}
	return nil, Group{}, fmt.Errorf("分组新增失败")
}

func (s *Server) updateGroup(id string, payload GroupUpdateRequest) (*Config, Group, error) {
	s.mu.RLock()
	cfg := cloneConfig(s.cfg)
	s.mu.RUnlock()

	groupIndex := findGroup(cfg, id)
	if groupIndex < 0 {
		return nil, Group{}, fmt.Errorf("分组不存在")
	}
	cfg.Groups[groupIndex].Name = payload.Name
	if err := cfg.NormalizeAndValidate(); err != nil {
		return nil, Group{}, err
	}
	return cfg, cfg.Groups[groupIndex], nil
}

func (s *Server) deleteGroup(id string) (*Config, error) {
	s.mu.RLock()
	cfg := cloneConfig(s.cfg)
	s.mu.RUnlock()

	groupIndex := findGroup(cfg, id)
	if groupIndex < 0 {
		return nil, fmt.Errorf("分组不存在")
	}
	if len(cfg.Groups) == 1 {
		return nil, fmt.Errorf("至少需要保留一个分组")
	}
	if len(cfg.Groups[groupIndex].Services) > 0 {
		return nil, fmt.Errorf("分组下还有入口，请先移动或删除入口")
	}
	cfg.Groups = append(cfg.Groups[:groupIndex], cfg.Groups[groupIndex+1:]...)
	if err := cfg.NormalizeAndValidate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (s *Server) initializeAuth(username, password, confirmPassword string) (*Config, error) {
	if username == "" {
		return nil, fmt.Errorf("账号不能为空")
	}
	if len(password) < 8 {
		return nil, fmt.Errorf("密码至少需要 8 位")
	}
	if password != confirmPassword {
		return nil, fmt.Errorf("两次输入的密码不一致")
	}
	secret, err := randomSessionSecret()
	if err != nil {
		return nil, fmt.Errorf("生成 session secret 失败: %w", err)
	}

	s.mu.RLock()
	cfg := cloneConfig(s.cfg)
	s.mu.RUnlock()
	if !configNeedsSetup(cfg) {
		return nil, fmt.Errorf("首次设置已完成")
	}
	cfg.Auth.Enabled = true
	cfg.Auth.Username = username
	cfg.Auth.Password = password
	cfg.Auth.SessionSecret = secret
	if cfg.Auth.SessionTTL == 0 {
		cfg.Auth.SessionTTL = 24 * time.Hour
	}
	if err := cfg.NormalizeAndValidate(); err != nil {
		return nil, err
	}
	return cfg, nil
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

func (s *Server) sortGroups(payload GroupSortRequest) (*Config, error) {
	s.mu.RLock()
	cfg := cloneConfig(s.cfg)
	s.mu.RUnlock()

	if len(payload.GroupIDs) != len(cfg.Groups) {
		return nil, fmt.Errorf("排序数据必须包含全部分组")
	}

	groupByID := make(map[string]Group, len(cfg.Groups))
	for _, group := range cfg.Groups {
		groupByID[group.ID] = group
	}
	seen := make(map[string]struct{}, len(cfg.Groups))
	nextGroups := make([]Group, 0, len(cfg.Groups))
	for _, rawGroupID := range payload.GroupIDs {
		groupID := strings.TrimSpace(rawGroupID)
		group, ok := groupByID[groupID]
		if !ok {
			return nil, fmt.Errorf("分组不存在")
		}
		if _, ok := seen[groupID]; ok {
			return nil, fmt.Errorf("排序数据包含重复分组")
		}
		seen[groupID] = struct{}{}
		nextGroups = append(nextGroups, group)
	}
	cfg.Groups = nextGroups
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
	cfg.Appearance.BackgroundOverlay = payload.BackgroundOverlay
	if err := cfg.NormalizeAndValidate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func findGroup(cfg *Config, id string) int {
	for gi, group := range cfg.Groups {
		if group.ID == id {
			return gi
		}
	}
	return -1
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

func uniqueGroupID(cfg *Config, name string) string {
	base := slugifyID(name)
	if base == "" {
		base = "group"
	}
	used := make(map[string]struct{}, len(cfg.Groups))
	for _, group := range cfg.Groups {
		used[group.ID] = struct{}{}
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
	overlay := backgroundOverlayAlpha(appearance.BackgroundOverlay)
	var b strings.Builder
	b.WriteString("background-color:")
	b.WriteString(color)
	b.WriteString(";")
	if appearance.BackgroundImage != "" {
		image := cssEscapePattern.ReplaceAllString(appearance.BackgroundImage, "")
		b.WriteString("background-image:linear-gradient(rgba(0,0,0,")
		b.WriteString(overlay)
		b.WriteString("),rgba(0,0,0,")
		b.WriteString(overlay)
		b.WriteString(")),url(\"")
		b.WriteString(image)
		b.WriteString("\");background-size:cover;background-position:center;background-attachment:fixed;")
	}
	return template.CSS(b.String())
}

func backgroundOverlayAlpha(value string) string {
	switch value {
	case "low":
		return ".18"
	case "high":
		return ".42"
	default:
		return ".30"
	}
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

const openRedirectTemplate = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="referrer" content="no-referrer">
  <title>Opening...</title>
</head>
<body>
  <p>正在打开链接。如果没有自动跳转，请使用下面的链接。</p>
  <p><a href="{{.Target}}" rel="noreferrer">打开链接</a></p>
  <script>
    const target = {{.Target}};
    setTimeout(() => { window.location.replace(target); }, 80);
  </script>
</body>
</html>
`

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

var templateIconNames = []string{
	"mdi:web",
	"mdi:lan",
	"mdi:content-save-outline",
	"mdi:folder-cog-outline",
	"mdi:image-multiple-outline",
	"mdi:image-edit-outline",
	"mdi:logout",
	"mdi:magnify",
	"mdi:plus",
	"mdi:cursor-default-click-outline",
	"mdi:open-in-new",
	"mdi:link-variant",
	"mdi:pencil-box-outline",
	"mdi:trash-can-outline",
	"mdi:close",
	"mdi:upload",
	"mdi:content-copy",
	"mdi:image-plus-outline",
	"mdi:wallpaper",
	"mdi:arrow-up",
	"mdi:arrow-down",
	"mdi:eye",
	"mdi:eye-off",
}

var fallbackIconText = map[string]string{
	"mdi:web":                          "◎",
	"mdi:lan":                          "⌘",
	"mdi:content-save-outline":         "□",
	"mdi:folder-cog-outline":           "▣",
	"mdi:image-multiple-outline":       "▧",
	"mdi:image-edit-outline":           "✎",
	"mdi:logout":                       "↪",
	"mdi:magnify":                      "⌕",
	"mdi:plus":                         "+",
	"mdi:cursor-default-click-outline": "✣",
	"mdi:open-in-new":                  "↗",
	"mdi:link-variant":                 "⌁",
	"mdi:pencil-box-outline":           "✎",
	"mdi:trash-can-outline":            "×",
	"mdi:close":                        "×",
	"mdi:upload":                       "↑",
	"mdi:content-copy":                 "⧉",
	"mdi:image-plus-outline":           "▧",
	"mdi:wallpaper":                    "▥",
	"mdi:arrow-up":                     "↑",
	"mdi:arrow-down":                   "↓",
	"mdi:eye":                          "◉",
	"mdi:eye-off":                      "◎",
}

var openRedirectTpl = template.Must(template.New("open").Parse(openRedirectTemplate))

func (s *Server) inlineIconJSON(icon string) template.JS {
	return template.JS(strconv.Quote(string(s.inlineIconHTML(icon))))
}

func (s *Server) inlineIconHTML(icon string) template.HTML {
	icon = strings.TrimSpace(icon)
	if icon == "" {
		return emptyInlineIconHTML()
	}

	s.iconMu.RLock()
	if html, ok := s.iconHTML[icon]; ok {
		s.iconMu.RUnlock()
		return html
	}
	s.iconMu.RUnlock()

	collection, iconName, ok := strings.Cut(icon, ":")
	if !ok || !iconifyPartPattern.MatchString(collection) || !iconifyPartPattern.MatchString(iconName) {
		return emptyInlineIconHTML()
	}
	cfg := s.currentConfig()
	if cfg.Assets.IconCacheDir == "" {
		return fallbackInlineIconHTML(icon)
	}
	body, cached, err := loadCachedIconifySVG(cfg.Assets.IconCacheDir, collection, iconName)
	if err != nil || !cached {
		return fallbackInlineIconHTML(icon)
	}
	html := inlineSVGHTML(body)

	s.iconMu.Lock()
	s.iconHTML[icon] = html
	s.iconMu.Unlock()
	return html
}

func (s *Server) serviceIconHTML(service Service) template.HTML {
	if service.IconIsOnline() {
		collection, iconName, ok := strings.Cut(service.Icon, ":")
		if ok && iconifyPartPattern.MatchString(collection) && iconifyPartPattern.MatchString(iconName) {
			cfg := s.currentConfig()
			body, cached, err := loadCachedIconifySVG(cfg.Assets.IconCacheDir, collection, iconName)
			if err == nil && cached {
				return inlineSVGHTML(body)
			}
		}
		return template.HTML(`<img src="` + template.HTMLEscapeString(service.IconImageSrc()) + `" alt="" loading="lazy" decoding="async">`)
	}
	if service.IconIsImage() {
		return template.HTML(`<img src="` + template.HTMLEscapeString(service.Icon) + `" alt="" loading="lazy" decoding="async">`)
	}
	return template.HTML(`<span class="icon-fallback">` + template.HTMLEscapeString(service.DisplayIconText()) + `</span>`)
}

func inlineSVGHTML(body []byte) template.HTML {
	svg := strings.TrimSpace(string(body))
	if !strings.Contains(svg, "<svg") {
		return emptyInlineIconHTML()
	}
	return template.HTML(`<span class="inline-icon" aria-hidden="true">` + svg + `</span>`)
}

func emptyInlineIconHTML() template.HTML {
	return template.HTML(`<span class="inline-icon" aria-hidden="true"></span>`)
}

func fallbackInlineIconHTML(icon string) template.HTML {
	text := fallbackIconText[icon]
	if text == "" {
		return emptyInlineIconHTML()
	}
	return template.HTML(`<span class="inline-icon inline-icon-fallback" aria-hidden="true">` + template.HTMLEscapeString(text) + `</span>`)
}

func (s *Server) prewarmIconCache(cfg *Config) {
	if cfg.Assets.IconCacheDir == "" {
		return
	}
	seen := make(map[string]struct{}, len(templateIconNames))
	for _, icon := range templateIconNames {
		seen[icon] = struct{}{}
	}
	for _, group := range cfg.Groups {
		for _, service := range group.Services {
			if service.IconIsOnline() {
				seen[service.Icon] = struct{}{}
			}
		}
	}

	sem := make(chan struct{}, 6)
	for icon := range seen {
		collection, iconName, ok := strings.Cut(icon, ":")
		if !ok || !iconifyPartPattern.MatchString(collection) || !iconifyPartPattern.MatchString(iconName) {
			continue
		}
		go func(collection, iconName string) {
			sem <- struct{}{}
			defer func() { <-sem }()
			_, _ = loadIconifySVG(context.Background(), cfg.Assets.IconCacheDir, collection, iconName)
		}(collection, iconName)
	}
}

const indexTemplate = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{.Title}}</title>
  <link rel="icon" href="/favicon.svg" type="image/svg+xml">
  <style>
    :root {
      color-scheme: dark;
      --bg: #000;
      --panel: #151515;
      --panel-2: #202026;
      --panel-3: #2c2c34;
      --control-bg: rgba(12, 12, 14, .58);
      --control-bg-hover: rgba(24, 24, 28, .72);
      --control-border: rgba(255, 255, 255, .16);
      --control-shadow: 0 10px 24px rgba(0, 0, 0, .22);
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
    .tool-button { width: 48px; height: 48px; border: 1px solid var(--control-border); border-radius: 8px; background: var(--control-bg); color: #fff; display: grid; place-items: center; cursor: pointer; box-shadow: var(--control-shadow); backdrop-filter: blur(14px) saturate(130%); -webkit-backdrop-filter: blur(14px) saturate(130%); }
	    .tool-button:hover { background: var(--control-bg-hover); }
	    .tool-button:disabled { cursor: default; opacity: .42; }
	    .tool-button:disabled:hover { background: var(--control-bg); }
	    .sort-button { display: none; }
	    body.is-edit-mode .sort-button { display: grid; }
	    .tool-button .inline-icon { font-size: 22px; }
    .groups { display: grid; gap: 56px; }
    .group { display: grid; gap: 24px; }
    .group-title { display: flex; align-items: center; justify-content: space-between; gap: 16px; }
    .group-heading { display: flex; align-items: baseline; gap: 12px; min-width: 0; }
    h2 { margin: 0; font-size: 24px; letter-spacing: 0; font-weight: 800; text-shadow: 0 2px 12px rgba(0,0,0,.38); }
    .group-count { color: var(--muted); font-size: 14px; text-shadow: 0 2px 10px rgba(0,0,0,.34); }
    .group-actions { display: flex; align-items: center; gap: 8px; }
    .group-action { width: 38px; height: 38px; border: 1px solid transparent; border-radius: 8px; background: transparent; color: #fff; display: grid; place-items: center; cursor: pointer; }
    .group-action:hover, .group-action.is-active { border-color: rgba(255,255,255,.35); background: rgba(255,255,255,.08); }
    .group-action .inline-icon { font-size: 26px; }
	    .icon-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(86px, 1fr)); gap: 28px 20px; align-items: start; }
	    .app-icon { display: grid; justify-items: center; gap: 8px; min-width: 0; color: #fff; }
	    body.is-edit-mode .app-icon { cursor: grab; touch-action: none; }
	    body.is-dragging, body.is-dragging * { user-select: none; }
	    body.is-dragging .app-icon { cursor: grabbing; }
	    body.is-dragging .app-icon:not(.is-dragging) { will-change: transform; }
	    .app-icon.is-dragging { position: fixed; left: 0; top: 0; z-index: 70; opacity: .96; pointer-events: none; filter: drop-shadow(0 22px 34px rgba(0,0,0,.46)); transform: translate3d(0,0,0); will-change: transform; }
	    body.is-dragging .app-icon.is-dragging .icon-button { transform: none; background: var(--control-bg-hover); }
	    .drag-placeholder { width: 100%; min-height: 122px; visibility: hidden; pointer-events: none; }
    .icon-button { width: 76px; height: 76px; border: 1px solid var(--control-border); border-radius: 14px; background: var(--control-bg); color: #fff; display: grid; place-items: center; cursor: pointer; transition: transform .12s ease, background .12s ease, border-color .12s ease; position: relative; box-shadow: var(--control-shadow); }
    .icon-button:hover { transform: translateY(-2px); background: var(--control-bg-hover); border-color: rgba(255,255,255,.25); }
    body.is-edit-mode .icon-button { outline: 2px dashed rgba(255,255,255,.55); outline-offset: 5px; }
    .icon-button:focus-visible { outline: 3px solid rgba(103, 224, 182, .45); outline-offset: 3px; }
    .icon-button .inline-icon { font-size: 42px; }
    .icon-button img { width: 50px; height: 50px; object-fit: contain; border-radius: 10px; }
    .inline-icon { display: inline-grid; place-items: center; width: 1em; height: 1em; line-height: 1; }
    .inline-icon svg { display: block; width: 1em; height: 1em; }
    .inline-icon-fallback { font-weight: 900; }
    .icon-fallback { font-size: 17px; font-weight: 800; max-width: 64px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .health-dot { position: absolute; right: 7px; bottom: 7px; width: 10px; height: 10px; border-radius: 50%; background: var(--unknown); box-shadow: 0 0 0 2px rgba(10,10,12,.70), 0 2px 8px rgba(0,0,0,.28); }
    .health-dot[data-status="healthy"] { background: var(--ok); }
    .health-dot[data-status="unhealthy"] { background: var(--bad); }
    .health-dot[data-status="disabled"] { background: var(--disabled); }
    .app-name { width: 92px; min-height: 38px; text-align: center; color: #fff; font-size: 15px; line-height: 1.35; overflow-wrap: anywhere; text-shadow: 0 2px 10px rgba(0,0,0,.50); }
    .empty { display: none; color: var(--muted); padding: 22px 0; }
    body.is-empty .empty { display: block; }
    .group.is-hidden { display: none; }
    .menu { position: fixed; z-index: 50; min-width: 292px; border-radius: 6px; background: #53535d; box-shadow: 0 18px 48px rgba(0,0,0,.45); padding: 20px 0 10px; display: none; }
    .menu.is-open { display: block; }
    .menu-section { padding: 0 24px 16px; }
    .menu-title { margin: 0 0 8px; color: #e8e8ef; font-size: 24px; font-weight: 700; }
    .menu-actions { display: flex; gap: 10px; }
    .menu-icon { width: 52px; height: 44px; border: 1px solid #82828b; border-radius: 5px; background: transparent; color: #f4f4f7; display: grid; place-items: center; cursor: pointer; }
    .menu-icon .inline-icon { font-size: 24px; }
    .menu-line { height: 1px; background: #696973; margin: 6px 0; }
    .menu-command { width: 100%; min-height: 54px; border: 0; background: transparent; color: #f4f4f7; display: flex; align-items: center; gap: 16px; padding: 0 24px; font-size: 22px; cursor: pointer; }
    .menu-command:hover, .menu-icon:hover { background: rgba(255,255,255,.08); }
    .menu-command .inline-icon { font-size: 28px; }
    .modal-backdrop { position: fixed; inset: 0; display: none; place-items: center; background: rgba(0,0,0,.62); z-index: 60; padding: 18px; }
    .modal-backdrop.is-open { display: grid; }
    .modal { width: min(1100px, 100%); max-height: min(860px, calc(100vh - 36px)); overflow: auto; border-radius: 28px; background: #2d2d35; color: #f7f7fb; box-shadow: 0 22px 80px rgba(0,0,0,.62); padding: 28px 32px 32px; }
    .confirm-backdrop { z-index: 80; background: rgba(0,0,0,.72); }
    .confirm-modal { width: min(480px, 100%); max-height: calc(100vh - 36px); overflow: auto; border-radius: 18px; background: #25252d; border: 1px solid #595a64; color: #f7f7fb; box-shadow: 0 26px 90px rgba(0,0,0,.68); padding: 26px; }
    .confirm-head { display: flex; align-items: center; gap: 14px; margin-bottom: 16px; }
    .confirm-mark { width: 46px; height: 46px; border-radius: 12px; background: rgba(231,125,130,.14); color: var(--danger); display: grid; place-items: center; flex: 0 0 auto; }
    .confirm-mark .inline-icon { font-size: 26px; }
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
    .close-button .inline-icon { font-size: 30px; }
    .preview { background: #c2c8d0; border: 1px solid #9fa5ad; border-radius: 22px; min-height: 180px; display: grid; grid-template-columns: 1fr 220px; gap: 28px; align-items: center; padding: 18px 26px; margin-bottom: 26px; color: #0b0b0f; }
    .preview-wide { justify-self: end; width: min(420px, 100%); height: 140px; border-radius: 28px; background: #888d90; display: flex; align-items: center; justify-content: center; gap: 34px; color: #d9dde0; font-size: 28px; font-weight: 800; }
    .preview-square { width: 140px; display: grid; gap: 10px; justify-items: center; font-size: 28px; color: #050506; }
    .preview-icon { width: 140px; height: 140px; border-radius: 28px; background: #878c8f; display: grid; place-items: center; color: #d9dde0; }
    .preview-icon .inline-icon { font-size: 62px; }
    .preview-icon img { width: 92px; height: 92px; object-fit: contain; }
    .form-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 22px 20px; }
    .field { display: grid; gap: 8px; min-width: 0; }
    .field.full { grid-column: 1 / -1; }
    .field label { font-size: 20px; font-weight: 800; color: #f0f0f4; }
    .field-link { margin-left: 10px; color: #83d8ff; font-size: 18px; font-weight: 600; }
    .field input, .field textarea, .field select { width: 100%; min-height: 56px; border: 1px solid transparent; border-radius: 6px; background: #48484f; color: #f3f3f7; padding: 0 18px; font-size: 20px; outline: 0; }
    .field textarea { min-height: 92px; padding-top: 14px; resize: vertical; line-height: 1.45; }
    .field input:focus, .field textarea:focus, .field select:focus { border-color: var(--accent); box-shadow: 0 0 0 3px rgba(103,224,182,.15); }
    .icon-field-row { display: grid; grid-template-columns: 1fr repeat(2, auto); gap: 10px; align-items: center; }
    .upload-button { min-height: 56px; border: 1px solid #73737d; border-radius: 6px; background: #3d3d45; color: #f3f3f7; padding: 0 18px; cursor: pointer; white-space: nowrap; }
    .upload-button:hover { background: #50505a; }
    .file-input { position: fixed; opacity: 0; pointer-events: none; width: 1px; height: 1px; }
    .form-actions { display: flex; justify-content: flex-end; gap: 14px; margin-top: 30px; }
    .save-button, .delete-button { min-width: 118px; min-height: 58px; border: 0; border-radius: 7px; font-size: 24px; cursor: pointer; }
    .save-button { background: var(--accent); color: #050505; }
    .delete-button { background: var(--danger); color: #050505; display: none; }
    .delete-button.is-visible { display: inline-block; }
    .group-manager-toolbar { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; margin-bottom: 18px; }
    .group-form { display: none; border: 1px solid #555761; border-radius: 14px; background: rgba(255,255,255,.04); padding: 18px; margin-bottom: 18px; }
    .group-form.is-open { display: block; }
    .group-form-row { display: grid; grid-template-columns: 1fr auto auto; gap: 12px; align-items: end; }
    .group-list { display: grid; gap: 10px; }
    .group-row { display: grid; grid-template-columns: 1fr auto; align-items: center; gap: 16px; min-height: 74px; border: 1px solid #454751; border-radius: 14px; background: #24242c; padding: 14px 16px; }
    .group-row-title { margin: 0; font-size: 20px; line-height: 1.25; overflow-wrap: anywhere; }
    .group-row-meta { margin-top: 4px; color: var(--muted); font-size: 14px; }
    .group-row-actions { display: flex; align-items: center; gap: 8px; }
    .row-icon-button { width: 40px; height: 40px; border: 1px solid #60616b; border-radius: 8px; background: transparent; color: #f4f4f7; display: grid; place-items: center; cursor: pointer; }
    .row-icon-button:hover { border-color: rgba(103,224,182,.72); color: var(--accent); background: rgba(255,255,255,.05); }
    .row-icon-button:disabled { cursor: default; opacity: .42; }
    .row-icon-button:disabled:hover { border-color: #60616b; color: #f4f4f7; background: transparent; }
    .row-icon-button[data-action="delete-group"]:hover { border-color: rgba(231,125,130,.74); color: var(--danger); }
    .row-icon-button .inline-icon { font-size: 22px; }
    .settings-preview { min-height: 170px; border: 1px solid #676873; border-radius: 16px; background: #000; display: grid; place-items: center; color: #fff; margin-bottom: 24px; overflow: hidden; }
    .settings-preview span { padding: 10px 16px; border-radius: 999px; background: rgba(0,0,0,.58); color: #fff; }
    .gallery-toolbar { display: flex; justify-content: space-between; align-items: center; gap: 14px; margin-bottom: 22px; flex-wrap: wrap; }
    .gallery-tabs { display: flex; align-items: center; border: 1px solid #696a75; border-radius: 8px; overflow: hidden; }
    .gallery-tab { min-height: 46px; border: 0; border-right: 1px solid #696a75; background: transparent; color: #ececf2; padding: 0 22px; font-size: 18px; font-weight: 800; cursor: pointer; }
    .gallery-tab:last-child { border-right: 0; }
    .gallery-tab.is-active { background: var(--accent); color: #07100d; }
    .gallery-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(210px, 1fr)); gap: 18px; min-height: 160px; }
    .gallery-card { min-width: 0; border: 1px solid #484a55; border-radius: 10px; background: #26262e; overflow: hidden; }
    .gallery-thumb { height: 132px; background-color: #bfc3ca; background-image: linear-gradient(45deg, rgba(255,255,255,.42) 25%, transparent 25%), linear-gradient(-45deg, rgba(255,255,255,.42) 25%, transparent 25%), linear-gradient(45deg, transparent 75%, rgba(255,255,255,.42) 75%), linear-gradient(-45deg, transparent 75%, rgba(255,255,255,.42) 75%); background-size: 24px 24px; background-position: 0 0, 0 12px, 12px -12px, -12px 0; display: grid; place-items: center; overflow: hidden; }
    .gallery-thumb img { width: 100%; height: 100%; object-fit: contain; }
    .gallery-body { padding: 12px 14px 14px; display: grid; gap: 10px; }
    .gallery-name { margin: 0; color: #f6f6fb; font-size: 16px; line-height: 1.35; font-weight: 800; overflow-wrap: anywhere; }
    .gallery-meta { display: flex; flex-wrap: wrap; gap: 6px; color: var(--muted); font-size: 13px; line-height: 1.3; }
    .gallery-badge { border: 1px solid #5a5c66; border-radius: 999px; padding: 3px 8px; }
    .gallery-badge.is-used { border-color: rgba(103,224,182,.72); color: var(--accent); }
    .gallery-upload-actions { display: flex; gap: 10px; flex-wrap: wrap; }
    .gallery-actions { display: flex; gap: 8px; flex-wrap: wrap; }
    .gallery-action { width: 38px; height: 36px; border: 1px solid #61636d; border-radius: 7px; background: #34343c; color: #f3f3f7; display: grid; place-items: center; cursor: pointer; }
    .gallery-action:hover { border-color: rgba(103,224,182,.72); color: var(--accent); }
    .gallery-action[data-action="delete-asset"]:hover { border-color: rgba(231,125,130,.74); color: var(--danger); }
    .gallery-action:disabled { opacity: .42; cursor: default; }
    .gallery-action .inline-icon { font-size: 21px; }
    .gallery-empty { display: none; border: 1px dashed #62636e; border-radius: 14px; color: var(--muted); padding: 28px; text-align: center; }
    .gallery-empty.is-visible { display: block; }
    .color-row { display: grid; grid-template-columns: 86px 1fr; gap: 12px; align-items: center; }
    .field input[type="color"] { min-height: 56px; padding: 6px; cursor: pointer; }
    .secondary-button { min-height: 58px; border: 1px solid #73737d; border-radius: 7px; background: #3d3d45; color: #f3f3f7; padding: 0 18px; font-size: 22px; cursor: pointer; }
    .secondary-button:hover { background: #50505a; }
    .toast { position: fixed; left: 50%; bottom: 28px; transform: translateX(-50%); background: #202026; color: #fff; border: 1px solid #555761; border-radius: 999px; padding: 10px 18px; display: none; z-index: 80; }
    .toast.is-open { display: block; }
    @media (max-width: 760px) {
      .shell { width: min(100vw - 24px, 1240px); padding-top: 92px; }
      .top-tools { top: 12px; right: 12px; }
      .group-title { align-items: flex-start; }
      .group-actions { padding-top: 2px; }
      .icon-grid { grid-template-columns: repeat(auto-fill, minmax(74px, 1fr)); gap: 22px 14px; }
      .icon-button { width: 66px; height: 66px; border-radius: 13px; }
      .icon-button .inline-icon { font-size: 36px; }
      .app-name { width: 78px; font-size: 13px; }
      .drag-placeholder { min-height: 109px; }
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
      .icon-field-row { grid-template-columns: 1fr; }
      .gallery-toolbar { align-items: stretch; }
      .gallery-tabs, .gallery-toolbar .upload-button, .gallery-upload-actions { width: 100%; }
      .gallery-upload-actions .upload-button { flex: 1 1 0; }
      .gallery-tab { flex: 1 1 0; padding: 0 10px; }
      .gallery-grid { grid-template-columns: 1fr; }
      .group-form-row, .group-row { grid-template-columns: 1fr; align-items: stretch; }
      .group-row-actions { justify-content: flex-end; flex-wrap: wrap; }
      .color-row { grid-template-columns: 70px 1fr; }
      .form-actions { justify-content: stretch; }
      .save-button, .secondary-button { width: 100%; }
    }
  </style>
</head>
<body style="{{.BackgroundCSS}}" data-background-color="{{.Appearance.BackgroundColor}}" data-background-image="{{.Appearance.BackgroundImage}}" data-background-overlay="{{.Appearance.BackgroundOverlay}}">
	  <div class="top-tools">
	    <button class="tool-button" type="button" id="access-mode-button" title="当前使用外网入口"><span id="access-mode-icon">{{icon "mdi:web"}}</span></button>
	    <button class="tool-button sort-button" type="button" id="save-sort-button" title="保存排序" disabled>{{icon "mdi:content-save-outline"}}</button>
	    <button class="tool-button" type="button" id="open-groups-button" title="分组管理">{{icon "mdi:folder-cog-outline"}}</button>
	    <button class="tool-button" type="button" id="open-gallery-button" title="图库">{{icon "mdi:image-multiple-outline"}}</button>
	    <button class="tool-button" type="button" id="open-settings-button" title="页面设置">{{icon "mdi:image-edit-outline"}}</button>
    {{if .Auth.Enabled}}<form method="post" action="/logout"><button class="tool-button" type="submit" title="退出登录">{{icon "mdi:logout"}}</button></form>{{end}}
  </div>
  <main class="shell">
    <section class="groups">
      {{range .Groups}}
      <section class="group" data-group-id="{{.ID}}">
        <div class="group-title">
	          <div class="group-heading"><h2>{{.Name}}</h2><span class="group-count"><span class="group-visible-count">0</span> / <span class="group-total-count">{{len .Services}}</span></span></div>
          <div class="group-actions">
            <button class="group-action" type="button" data-action="manage-groups" title="分组管理">{{icon "mdi:folder-cog-outline"}}</button>
            <button class="group-action" type="button" data-action="add-service" data-group-id="{{.ID}}" title="新增入口">{{icon "mdi:plus"}}</button>
            <button class="group-action edit-mode-button" type="button" data-action="toggle-edit-mode" title="编辑模式">{{icon "mdi:cursor-default-click-outline"}}</button>
          </div>
        </div>
        <div class="icon-grid">
          {{range .Services}}
          <div class="app-icon" data-service-id="{{.ID}}" data-group-id="{{.GroupID}}" data-name="{{.Name}}" data-description="{{.Description}}" data-icon-text="{{.IconText}}" data-icon-value="{{.Icon}}" data-internal-url="{{.InternalURL}}" data-external-url="{{.ExternalURL}}" data-tags="{{range $i, $tag := .Tags}}{{if $i}},{{end}}{{.}}{{end}}" data-notes="{{.Notes}}" data-health-type="{{.Health.Type}}" data-health-url="{{.Health.URL}}" data-health-address="{{.Health.Address}}" data-health-expect-status="{{.Health.ExpectStatus}}" data-health-timeout="{{.Health.Timeout}}">
            <a class="icon-button" href="{{openHref .DefaultURL}}" target="_blank" rel="noopener noreferrer" aria-label="{{.Name}}">
              {{serviceIcon .}}
              <span class="health-dot" data-status="unknown"></span>
            </a>
            <div class="app-name">{{.Name}}</div>
          </div>
          {{end}}
        </div>
      </section>
      {{end}}
    </section>
    <p class="empty">暂无服务入口。</p>
  </main>

  <div class="menu" id="item-menu" role="menu" aria-hidden="true">
    <div class="menu-section"><p class="menu-title">打开外网入口</p><div class="menu-actions"><button class="menu-icon" type="button" data-action="open-external">{{icon "mdi:open-in-new"}}</button><button class="menu-icon" type="button" data-action="copy-external">{{icon "mdi:link-variant"}}</button></div></div>
    <div class="menu-section"><p class="menu-title">打开内网入口</p><div class="menu-actions"><button class="menu-icon" type="button" data-action="open-internal">{{icon "mdi:open-in-new"}}</button><button class="menu-icon" type="button" data-action="copy-internal">{{icon "mdi:link-variant"}}</button></div></div>
    <div class="menu-line"></div>
    <button class="menu-command" type="button" data-action="edit">{{icon "mdi:pencil-box-outline"}}编辑</button>
    <button class="menu-command" type="button" data-action="delete">{{icon "mdi:trash-can-outline"}}删除</button>
  </div>

  <div class="modal-backdrop" id="edit-backdrop">
    <section class="modal" role="dialog" aria-modal="true" aria-labelledby="edit-title">
      <div class="modal-head"><h2 class="modal-title" id="edit-title">编辑入口</h2><button class="close-button" type="button" id="edit-close" aria-label="关闭">{{icon "mdi:close"}}</button></div>
      <div class="preview"><div class="preview-wide"><div class="preview-icon" id="preview-wide-icon"></div><span id="preview-wide-name">-</span></div><div class="preview-square"><div class="preview-icon" id="preview-square-icon"></div><span id="preview-square-name">-</span></div></div>
      <form id="edit-form">
        <input type="hidden" name="id">
        <div class="form-grid">
          <div class="field"><label>名称 *</label><input name="name" maxlength="80" required></div>
          <div class="field"><label>描述</label><input name="description" maxlength="140"></div>
          <div class="field"><label>图标文字</label><input name="icon_text" maxlength="12" placeholder="NAS"></div>
          <div class="field"><label>在线图标名或图片 URL <a class="field-link" href="https://icon-sets.iconify.design/" target="_blank" rel="noreferrer">在线图标库</a></label><div class="icon-field-row"><input name="icon" placeholder="mdi:nas"><button class="upload-button" type="button" id="open-icon-gallery-button">图库</button><button class="upload-button" type="button" id="upload-icon-button">上传图片</button></div><input class="file-input" id="upload-icon-file" type="file" accept="image/png,image/jpeg,image/webp,image/gif,image/svg+xml,image/x-icon"></div>
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
	      <div class="modal-head"><h2 class="modal-title" id="settings-title">页面设置</h2><button class="close-button" type="button" id="settings-close" aria-label="关闭">{{icon "mdi:close"}}</button></div>
	      <div class="settings-preview" id="settings-preview"><span>背景预览</span></div>
      <form id="settings-form">
        <div class="form-grid">
          <div class="field"><label>背景颜色</label><div class="color-row"><input name="background_color_picker" type="color"><input name="background_color" placeholder="#000000"></div></div>
          <div class="field"><label>背景图片</label><div class="icon-field-row"><input name="background_image" placeholder="/uploads/background.png 或 https://example.com/bg.jpg"><button class="upload-button" type="button" id="open-background-gallery-button">图库</button><button class="upload-button" type="button" id="upload-background-button">上传图片</button></div><input class="file-input" id="upload-background-file" type="file" accept="image/png,image/jpeg,image/webp,image/gif,image/svg+xml"></div>
          <div class="field"><label>壁纸遮罩</label><select name="background_overlay"><option value="low">低</option><option value="medium">中</option><option value="high">高</option></select></div>
        </div>
        <div class="form-actions"><button class="secondary-button" type="button" id="reset-background-button">恢复默认</button><button class="save-button" type="submit">保存</button></div>
	      </form>
	    </section>
	  </div>
  <div class="modal-backdrop" id="gallery-backdrop">
    <section class="modal" role="dialog" aria-modal="true" aria-labelledby="gallery-title">
      <div class="modal-head"><h2 class="modal-title" id="gallery-title">图库</h2><button class="close-button" type="button" id="gallery-close" aria-label="关闭">{{icon "mdi:close"}}</button></div>
      <div class="gallery-toolbar">
        <div class="gallery-tabs" role="tablist" aria-label="图库筛选">
          <button class="gallery-tab is-active" type="button" data-gallery-filter="all">全部</button>
          <button class="gallery-tab" type="button" data-gallery-filter="wallpaper">壁纸</button>
          <button class="gallery-tab" type="button" data-gallery-filter="icon">图标</button>
        </div>
        <div class="gallery-upload-actions">
          <button class="upload-button" type="button" id="gallery-upload-icon-button" data-upload-type="icon">{{icon "mdi:upload"}} 上传图标</button>
          <button class="upload-button" type="button" id="gallery-upload-wallpaper-button" data-upload-type="wallpaper">{{icon "mdi:upload"}} 上传壁纸</button>
        </div>
        <input class="file-input" id="gallery-upload-file" type="file" accept="image/png,image/jpeg,image/webp,image/gif,image/svg+xml,image/x-icon">
      </div>
      <div class="gallery-empty" id="gallery-empty">暂无可用图片资源。</div>
      <div class="gallery-grid" id="gallery-grid"></div>
    </section>
  </div>
  <div class="modal-backdrop" id="groups-backdrop">
    <section class="modal" role="dialog" aria-modal="true" aria-labelledby="groups-title">
      <div class="modal-head"><h2 class="modal-title" id="groups-title">分组管理</h2><button class="close-button" type="button" id="groups-close" aria-label="关闭">{{icon "mdi:close"}}</button></div>
      <div class="group-manager-toolbar">
        <button class="secondary-button" type="button" id="add-group-button">{{icon "mdi:plus"}} 新增分组</button>
        <button class="secondary-button" type="button" id="save-group-sort-button" disabled>{{icon "mdi:content-save-outline"}} 保存排序</button>
      </div>
      <form id="group-form" class="group-form">
        <input type="hidden" name="id">
        <div class="group-form-row">
          <div class="field"><label>分组名称 *</label><input name="name" maxlength="80" required></div>
          <button class="secondary-button" type="button" id="cancel-group-button">取消</button>
          <button class="save-button" type="submit" id="save-group-button">保存</button>
        </div>
      </form>
      <div class="group-list" id="group-list">
        {{range .Groups}}
        <article class="group-row" data-group-id="{{.ID}}" data-group-name="{{.Name}}" data-service-count="{{len .Services}}">
          <div><h3 class="group-row-title">{{.Name}}</h3><div class="group-row-meta">{{len .Services}} 个入口</div></div>
          <div class="group-row-actions">
            <button class="row-icon-button" type="button" data-action="move-group-up" title="上移">{{icon "mdi:arrow-up"}}</button>
            <button class="row-icon-button" type="button" data-action="move-group-down" title="下移">{{icon "mdi:arrow-down"}}</button>
            <button class="row-icon-button" type="button" data-action="edit-group" title="编辑">{{icon "mdi:pencil-box-outline"}}</button>
            <button class="row-icon-button" type="button" data-action="delete-group" title="删除">{{icon "mdi:trash-can-outline"}}</button>
          </div>
        </article>
        {{end}}
      </div>
    </section>
  </div>
	  <div class="modal-backdrop confirm-backdrop" id="delete-confirm-backdrop" aria-hidden="true">
	    <section class="confirm-modal" role="dialog" aria-modal="true" aria-labelledby="delete-confirm-title" aria-describedby="delete-confirm-description">
	      <div class="confirm-head"><div class="confirm-mark">{{icon "mdi:trash-can-outline"}}</div><h2 class="confirm-title" id="delete-confirm-title">删除导航入口</h2></div>
	      <p class="confirm-body" id="delete-confirm-description">确定删除导航入口 <span class="confirm-name" id="delete-confirm-name">当前入口</span> 吗？</p>
      <p class="confirm-note">只会从导航配置里移除入口，不会删除、停止或重启真实服务。</p>
      <div class="confirm-actions"><button class="confirm-cancel" type="button" id="cancel-delete-button">取消</button><button class="confirm-delete" type="button" id="confirm-delete-button">删除</button></div>
    </section>
  </div>
  <div class="toast" id="toast"></div>

  <script>
    const items = [...document.querySelectorAll('.app-icon')];
    const groups = [...document.querySelectorAll('.group')];
    const menu = document.querySelector('#item-menu');
	    const backdrop = document.querySelector('#edit-backdrop');
	    const settingsBackdrop = document.querySelector('#settings-backdrop');
	    const galleryBackdrop = document.querySelector('#gallery-backdrop');
	    const groupsBackdrop = document.querySelector('#groups-backdrop');
	    const deleteConfirmBackdrop = document.querySelector('#delete-confirm-backdrop');
	    const form = document.querySelector('#edit-form');
	    const settingsForm = document.querySelector('#settings-form');
	    const groupForm = document.querySelector('#group-form');
	    const groupList = document.querySelector('#group-list');
	    const toast = document.querySelector('#toast');
    const uploadButton = document.querySelector('#upload-icon-button');
    const uploadFile = document.querySelector('#upload-icon-file');
    const uploadBackgroundButton = document.querySelector('#upload-background-button');
    const uploadBackgroundFile = document.querySelector('#upload-background-file');
    const openIconGalleryButton = document.querySelector('#open-icon-gallery-button');
    const openBackgroundGalleryButton = document.querySelector('#open-background-gallery-button');
    const galleryUploadButtons = [...document.querySelectorAll('[data-upload-type]')];
    const galleryUploadFile = document.querySelector('#gallery-upload-file');
    const galleryGrid = document.querySelector('#gallery-grid');
    const galleryEmpty = document.querySelector('#gallery-empty');
    const settingsPreview = document.querySelector('#settings-preview');
    const editTitle = document.querySelector('#edit-title');
    const saveButton = form.querySelector('.save-button');
    const deleteButton = document.querySelector('#delete-service-button');
    const cancelDeleteButton = document.querySelector('#cancel-delete-button');
	    const confirmDeleteButton = document.querySelector('#confirm-delete-button');
	    const deleteConfirmTitle = document.querySelector('#delete-confirm-title');
	    const deleteConfirmDescription = document.querySelector('#delete-confirm-description');
	    const deleteConfirmNote = document.querySelector('.confirm-note');
	    const deleteConfirmName = document.querySelector('#delete-confirm-name');
	    const saveSortButton = document.querySelector('#save-sort-button');
	    const saveGroupSortButton = document.querySelector('#save-group-sort-button');
	    const accessModeButton = document.querySelector('#access-mode-button');
    const accessModeIcon = document.querySelector('#access-mode-icon');
    const statusLabels = { healthy: '正常', unhealthy: '异常', unknown: '未知', disabled: '未启用' };
    const accessModeKey = 'home-nav.access-mode';
    let activeItem = null;
    let editMode = false;
    let accessMode = 'external';
    let sortDirty = false;
	    let sortSaving = false;
	    let sortSaveQueued = false;
	    const sortAnimations = new WeakMap();
	    let groupSortDirty = false;
	    let groupSortSaving = false;
	    let galleryAssets = [];
	    let galleryFilter = 'all';
	    let galleryMode = 'browse';
	    let galleryPendingUploadType = 'icon';
	    let dragState = null;
	    let suppressNextEditClick = false;
	    let pendingDelete = null;

	    function field(name) { return form.elements.namedItem(name); }
	    function settingField(name) { return settingsForm.elements.namedItem(name); }
	    function groupField(name) { return groupForm.elements.namedItem(name); }
    function showToast(message) { toast.textContent = message; toast.classList.add('is-open'); setTimeout(() => toast.classList.remove('is-open'), 1800); }
    function itemURL(type) { return type === 'internal' ? activeItem?.dataset.internalUrl : activeItem?.dataset.externalUrl; }
    function openHref(url) { return url || '#'; }
    function openRedirectHref(url) { return url ? '/open?url=' + encodeURIComponent(url) : '#'; }
    function openEntryURL(url) { url ? window.open(openRedirectHref(url), '_blank', 'noopener,noreferrer') : showToast('没有可用入口'); }
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
      accessModeIcon.innerHTML = accessMode === 'internal' ? {{iconJSON "mdi:lan"}} : {{iconJSON "mdi:web"}};
      for (const item of items) {
        const link = item.querySelector('.icon-button');
        const url = preferredURL(item, accessMode);
        link.href = openHref(url);
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
    function updateGroupCounts() {
      let visibleTotal = 0;
	      for (const group of groups) {
	        const totalCount = group.querySelectorAll('.app-icon').length;
	        const visibleCount = totalCount;
	        group.classList.toggle('is-hidden', visibleCount === 0);
	        group.querySelector('.group-visible-count').textContent = String(visibleCount);
	        group.querySelector('.group-total-count').textContent = String(totalCount);
	        visibleTotal += visibleCount;
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
	      saveSortButton.disabled = sortSaving || !sortDirty;
	      showToast(editMode ? '编辑模式已开启' : (sortDirty ? '编辑模式已关闭，排序未保存' : '编辑模式已关闭'));
	    }
	    function suppressEditClickOnce() {
	      suppressNextEditClick = true;
	      window.setTimeout(() => { suppressNextEditClick = false; }, 240);
	    }
	    function setSortDirty(value) {
	      sortDirty = value;
	      saveSortButton.disabled = sortSaving || !sortDirty;
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
    function backgroundOverlayAlpha(value) {
      if (value === 'low') return '.18';
      if (value === 'high') return '.42';
      return '.30';
    }
    function backgroundStyle(color, image, overlay) {
      const style = { backgroundColor: color || '#000000', backgroundImage: '', backgroundSize: '', backgroundPosition: '', backgroundAttachment: '' };
      if (image) {
        const alpha = backgroundOverlayAlpha(overlay);
        style.backgroundImage = 'linear-gradient(rgba(0,0,0,' + alpha + '),rgba(0,0,0,' + alpha + ')),url("' + String(image).replace(/["\\\n\r]/g, '') + '")';
        style.backgroundSize = 'cover';
        style.backgroundPosition = 'center';
        style.backgroundAttachment = 'fixed';
      }
      return style;
    }
    function applyBackgroundPreview(color, image, overlay, target) {
      const style = backgroundStyle(color, image, overlay);
      target.style.backgroundColor = style.backgroundColor;
      target.style.backgroundImage = style.backgroundImage;
      target.style.backgroundSize = style.backgroundSize;
      target.style.backgroundPosition = style.backgroundPosition;
      target.style.backgroundAttachment = '';
    }
    function applyBodyBackground(color, image, overlay) {
      const style = backgroundStyle(color, image, overlay);
      document.body.style.backgroundColor = style.backgroundColor;
      document.body.style.backgroundImage = style.backgroundImage;
      document.body.style.backgroundSize = style.backgroundSize;
      document.body.style.backgroundPosition = style.backgroundPosition;
      document.body.style.backgroundAttachment = style.backgroundAttachment;
    }
    function openSettings() {
      const color = document.body.dataset.backgroundColor || '#000000';
      const image = document.body.dataset.backgroundImage || '';
      const overlay = document.body.dataset.backgroundOverlay || 'medium';
      settingField('background_color').value = color;
      settingField('background_color_picker').value = normalizeHexColor(color);
      settingField('background_image').value = image;
      settingField('background_overlay').value = overlay;
      applyBackgroundPreview(color, image, overlay, settingsPreview);
      settingsBackdrop.classList.add('is-open');
	    }
	    function closeSettings() { settingsBackdrop.classList.remove('is-open'); }
	    function assetTypeLabel(type) {
	      if (type === 'wallpaper') return '壁纸';
	      if (type === 'icon') return '图标';
	      return '图片';
	    }
	    function formatBytes(size) {
	      if (!Number.isFinite(size) || size <= 0) return '0 B';
	      if (size < 1024) return size + ' B';
	      if (size < 1024 * 1024) return (size / 1024).toFixed(size < 10 * 1024 ? 1 : 0) + ' KB';
	      return (size / 1024 / 1024).toFixed(1) + ' MB';
	    }
	    function galleryVisibleAssets() {
	      return galleryAssets.filter(asset => galleryFilter === 'all' || asset.type === galleryFilter);
	    }
	    function renderGallery() {
	      const visible = galleryVisibleAssets();
	      galleryGrid.innerHTML = visible.map(asset => {
	        const dims = asset.width && asset.height ? asset.width + 'x' + asset.height : '';
	        const used = Array.isArray(asset.used_by) && asset.used_by.length ? asset.used_by.join('，') : '';
	        const canUseIcon = galleryMode === 'icon';
	        const canUseBackground = galleryMode === 'background';
	        return '<article class="gallery-card" data-url="' + escapeHTML(asset.url) + '" data-type="' + escapeHTML(asset.type) + '">' +
	          '<div class="gallery-thumb"><img src="' + escapeHTML(asset.url) + '" alt=""></div>' +
	          '<div class="gallery-body">' +
	            '<p class="gallery-name">' + escapeHTML(asset.name || asset.url) + '</p>' +
	            '<div class="gallery-meta">' +
	              '<span class="gallery-badge">' + assetTypeLabel(asset.type) + '</span>' +
	              '<span class="gallery-badge">' + formatBytes(Number(asset.size || 0)) + '</span>' +
	              (dims ? '<span class="gallery-badge">' + escapeHTML(dims) + '</span>' : '') +
	              (used ? '<span class="gallery-badge is-used" title="' + escapeHTML(used) + '">使用中</span>' : '') +
	            '</div>' +
	            '<div class="gallery-actions">' +
	              '<button class="gallery-action" type="button" data-action="copy-asset" title="复制路径">{{icon "mdi:content-copy"}}</button>' +
	              (canUseIcon ? '<button class="gallery-action" type="button" data-action="use-icon-asset" title="填入图标">{{icon "mdi:image-plus-outline"}}</button>' : '') +
	              (canUseBackground ? '<button class="gallery-action" type="button" data-action="use-background-asset" title="设为背景">{{icon "mdi:wallpaper"}}</button>' : '') +
	              '<button class="gallery-action" type="button" data-action="delete-asset" title="' + (used ? '资源使用中' : '删除资源') + '"' + (used ? ' disabled' : '') + '>{{icon "mdi:trash-can-outline"}}</button>' +
	            '</div>' +
	          '</div>' +
	        '</article>';
	      }).join('');
	      galleryEmpty.textContent = galleryAssets.length ? '当前筛选下没有图片资源。' : '暂无可用图片资源。';
	      galleryEmpty.classList.toggle('is-visible', visible.length === 0);
	    }
	    async function loadGallery() {
	      galleryGrid.innerHTML = '';
	      galleryEmpty.textContent = '正在读取图库...';
	      galleryEmpty.classList.add('is-visible');
	      const response = await fetch('/api/assets', { cache: 'no-store' });
	      const payload = await response.json().catch(() => ({}));
	      if (!response.ok) {
	        galleryAssets = [];
	        galleryEmpty.textContent = payload.error || '读取图库失败';
	        galleryEmpty.classList.add('is-visible');
	        return;
	      }
	      galleryAssets = Array.isArray(payload.assets) ? payload.assets : [];
	      renderGallery();
	    }
	    function setGalleryFilter(value) {
	      galleryFilter = value === 'wallpaper' || value === 'icon' ? value : 'all';
	      for (const tab of document.querySelectorAll('.gallery-tab')) tab.classList.toggle('is-active', tab.dataset.galleryFilter === galleryFilter);
	      updateGalleryUploadControls();
	      renderGallery();
	    }
	    function setGalleryUploadButtonState(disabled) {
	      for (const button of galleryUploadButtons) button.disabled = disabled;
	    }
	    function resetGalleryUploadButtonLabels() {
	      for (const button of galleryUploadButtons) {
	        const type = button.dataset.uploadType;
	        if (galleryMode === 'icon') button.innerHTML = '{{icon "mdi:upload"}} 上传并填入图标';
	        else if (galleryMode === 'background') button.innerHTML = '{{icon "mdi:upload"}} 上传并设为背景';
	        else button.innerHTML = type === 'wallpaper' ? '{{icon "mdi:upload"}} 上传壁纸' : '{{icon "mdi:upload"}} 上传图标';
	      }
	    }
	    function updateGalleryUploadControls() {
	      for (const button of galleryUploadButtons) {
	        const type = button.dataset.uploadType;
	        button.hidden = (galleryMode === 'icon' && type !== 'icon') || (galleryMode === 'background' && type !== 'wallpaper');
	      }
	      resetGalleryUploadButtonLabels();
	    }
	    function openGallery(mode = 'browse') {
	      galleryMode = mode;
	      if (mode === 'icon') setGalleryFilter('icon');
	      else if (mode === 'background') setGalleryFilter('wallpaper');
	      else setGalleryFilter('all');
	      galleryBackdrop.classList.add('is-open');
	      loadGallery();
	    }
	    function closeGallery() { galleryBackdrop.classList.remove('is-open'); }
	    async function setBackgroundImageFromAsset(asset) {
	      const payload = {
	        background_color: document.body.dataset.backgroundColor || '#000000',
	        background_image: asset.url,
	        background_overlay: document.body.dataset.backgroundOverlay || 'medium'
	      };
	      const response = await fetch('/api/settings', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload) });
	      const result = await response.json().catch(() => ({}));
	      if (!response.ok) {
	        showToast(result.error || '设置背景失败');
	        return;
	      }
	      document.body.dataset.backgroundColor = payload.background_color;
	      document.body.dataset.backgroundImage = payload.background_image;
	      document.body.dataset.backgroundOverlay = payload.background_overlay;
	      applyBodyBackground(payload.background_color, payload.background_image, payload.background_overlay);
	      if (settingsBackdrop.classList.contains('is-open')) {
	        settingField('background_image').value = payload.background_image;
	        settingField('background_overlay').value = payload.background_overlay;
	        applyBackgroundPreview(payload.background_color, payload.background_image, payload.background_overlay, settingsPreview);
	      }
	      showToast('背景已更新');
	      await loadGallery();
	    }
	    async function uploadGalleryAsset(file, assetType) {
	      if (!file) return;
	      const formData = new FormData();
	      formData.append('file', file);
	      formData.append('asset_type', assetType);
	      setGalleryUploadButtonState(true);
	      for (const button of galleryUploadButtons) {
	        if (button.dataset.uploadType === assetType) button.textContent = '上传中';
	      }
	      try {
	        const response = await fetch('/api/uploads', { method: 'POST', body: formData });
	        const payload = await response.json().catch(() => ({}));
	        if (!response.ok) {
	          showToast(payload.error || '上传失败');
	          return;
	        }
	        const uploaded = { url: payload.url || '', type: assetType };
	        if (galleryMode === 'icon') {
	          field('icon').value = uploaded.url;
	          refreshPreview();
	          closeGallery();
	          showToast('图标已上传并填入');
	          return;
	        }
	        if (galleryMode === 'background') {
	          await setBackgroundImageFromAsset(uploaded);
	          closeGallery();
	          return;
	        }
	        showToast(assetType === 'wallpaper' ? '壁纸已上传' : '图标已上传');
	        await loadGallery();
	      } finally {
	        setGalleryUploadButtonState(false);
	        resetGalleryUploadButtonLabels();
	        galleryUploadFile.value = '';
	      }
	    }
	    function openGroups() {
	      refreshGroupMoveButtons();
	      groupsBackdrop.classList.add('is-open');
	      closeMenu();
	    }
	    function closeGroups() {
	      groupsBackdrop.classList.remove('is-open');
	      closeGroupForm();
	    }
	    function openGroupForm(row) {
	      const editing = Boolean(row);
	      groupField('id').value = editing ? row.dataset.groupId : '';
	      groupField('name').value = editing ? (row.dataset.groupName || '') : '';
	      document.querySelector('#save-group-button').textContent = editing ? '保存' : '新增';
	      groupForm.classList.add('is-open');
	      groupField('name').focus();
	    }
	    function closeGroupForm() {
	      groupForm.reset();
	      groupField('id').value = '';
	      groupForm.classList.remove('is-open');
	    }
	    function groupRows() { return [...groupList.querySelectorAll('.group-row')]; }
	    function refreshGroupMoveButtons() {
	      const rows = groupRows();
	      rows.forEach((row, index) => {
	        row.querySelector('[data-action="move-group-up"]').disabled = index === 0;
	        row.querySelector('[data-action="move-group-down"]').disabled = index === rows.length - 1;
	      });
	    }
	    function setGroupSortDirty(value) {
	      groupSortDirty = value;
	      saveGroupSortButton.disabled = groupSortSaving || !groupSortDirty;
	    }
	    function groupSortPayload() {
	      return { group_ids: groupRows().map(row => row.dataset.groupId) };
	    }
	    async function saveGroup(event) {
	      event.preventDefault();
	      const id = groupField('id').value;
	      const payload = { name: groupField('name').value };
	      const url = id ? '/api/groups/' + encodeURIComponent(id) : '/api/groups';
	      const method = id ? 'PUT' : 'POST';
	      const response = await fetch(url, { method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload) });
	      if (!response.ok) {
	        const error = await response.json().catch(() => ({ error: '保存分组失败' }));
	        showToast(error.error || '保存分组失败');
	        return;
	      }
	      showToast(id ? '分组已保存' : '分组已新增');
	      setTimeout(() => location.reload(), 500);
	    }
	    async function saveGroupSort() {
	      if (groupSortSaving || !groupSortDirty) return;
	      const payload = groupSortPayload();
	      groupSortSaving = true;
	      saveGroupSortButton.disabled = true;
	      try {
	        const response = await fetch('/api/groups/sort', {
	          method: 'PUT',
	          headers: { 'Content-Type': 'application/json' },
	          body: JSON.stringify(payload)
	        });
	        if (!response.ok) {
	          const error = await response.json().catch(() => ({ error: '保存分组排序失败' }));
	          showToast(error.error || '保存分组排序失败');
	          setGroupSortDirty(true);
	          return;
	        }
	        setGroupSortDirty(false);
	        showToast('分组排序已保存');
	        setTimeout(() => location.reload(), 500);
	      } finally {
	        groupSortSaving = false;
	        saveGroupSortButton.disabled = !groupSortDirty;
	      }
	    }
	    function moveGroup(row, direction) {
	      if (!row) return;
	      if (direction < 0 && row.previousElementSibling) {
	        groupList.insertBefore(row, row.previousElementSibling);
	        setGroupSortDirty(true);
	      }
	      if (direction > 0 && row.nextElementSibling) {
	        groupList.insertBefore(row.nextElementSibling, row);
	        setGroupSortDirty(true);
	      }
	      refreshGroupMoveButtons();
	    }
	    function copyTextFallback(value) {
	      const previousFocus = document.activeElement;
	      const textarea = document.createElement('textarea');
	      textarea.value = value;
	      textarea.setAttribute('readonly', '');
	      textarea.style.position = 'fixed';
	      textarea.style.top = '-9999px';
	      textarea.style.left = '-9999px';
	      document.body.appendChild(textarea);
	      textarea.focus();
	      textarea.select();
	      textarea.setSelectionRange(0, textarea.value.length);
	      let ok = false;
	      try {
	        ok = document.execCommand('copy');
	      } catch (_) {
	        ok = false;
	      }
	      document.body.removeChild(textarea);
	      if (previousFocus && typeof previousFocus.focus === 'function') previousFocus.focus();
	      return ok;
	    }
	    function showManualCopy(value) {
	      window.prompt('请手动复制链接', value);
	      showToast('复制失败，请手动复制');
	    }
	    async function copyText(value) {
	      if (!value) return showToast('没有可复制的链接');
	      if (navigator.clipboard?.writeText && window.isSecureContext) {
	        try {
	          await navigator.clipboard.writeText(value);
	          showToast('链接已复制');
	          return;
	        } catch (_) {}
	      }
	      if (copyTextFallback(value)) {
	        showToast('链接已复制');
	        return;
	      }
	      showManualCopy(value);
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
	    function openDeleteConfirm(id, name, type = 'service') {
	      if (!id) return;
	      pendingDelete = { id, type, name: name || (type === 'group' ? '当前分组' : '当前入口') };
	      const isGroup = pendingDelete.type === 'group';
	      const isAsset = pendingDelete.type === 'asset';
	      deleteConfirmTitle.textContent = isAsset ? '删除图库资源' : (isGroup ? '删除分组' : '删除导航入口');
	      deleteConfirmDescription.firstChild.nodeValue = isAsset ? '确定删除图库资源 ' : (isGroup ? '确定删除分组 ' : '确定删除导航入口 ');
	      deleteConfirmName.textContent = '“' + pendingDelete.name + '”';
	      deleteConfirmNote.textContent = isAsset ? '只会删除 uploads 目录内的图片文件；正在被背景或入口图标引用的资源不能删除。' : (isGroup ? '只能删除空分组；含有入口的分组需要先移动或删除入口。' : '只会从导航配置里移除入口，不会删除、停止或重启真实服务。');
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
	      const isGroup = pendingDelete.type === 'group';
	      const isAsset = pendingDelete.type === 'asset';
	      confirmDeleteButton.disabled = true;
	      confirmDeleteButton.textContent = '删除中';
	      const response = await fetch(isAsset ? '/api/assets?url=' + encodeURIComponent(id) : ((isGroup ? '/api/groups/' : '/api/services/') + encodeURIComponent(id)), { method: 'DELETE' });
	      if (!response.ok) {
	        const error = await response.json().catch(() => ({ error: isAsset ? '删除资源失败' : (isGroup ? '删除分组失败' : '删除失败') }));
	        showToast(error.error || (isAsset ? '删除资源失败' : (isGroup ? '删除分组失败' : '删除失败')));
	        confirmDeleteButton.disabled = false;
	        confirmDeleteButton.textContent = '删除';
	        return;
	      }
	      showToast(isAsset ? '资源已删除' : (isGroup ? '分组已删除' : '已删除'));
	      closeDeleteConfirm();
	      if (isAsset) {
	        await loadGallery();
	        return;
	      }
	      setTimeout(() => location.reload(), 500);
	    }
	    async function deleteItem() {
	      openDeleteConfirm(field('id').value, field('name').value, 'service');
	    }
    async function saveSettings(event) {
      event.preventDefault();
      const payload = {
        background_color: settingField('background_color').value,
        background_image: settingField('background_image').value,
        background_overlay: settingField('background_overlay').value
      };
      const response = await fetch('/api/settings', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload) });
      if (!response.ok) {
        const error = await response.json().catch(() => ({ error: '保存失败' }));
        showToast(error.error || '保存失败');
        return;
      }
      document.body.dataset.backgroundColor = payload.background_color;
      document.body.dataset.backgroundImage = payload.background_image;
      document.body.dataset.backgroundOverlay = payload.background_overlay;
      applyBodyBackground(payload.background_color, payload.background_image, payload.background_overlay);
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
	    async function saveSort(options = {}) {
	      if (sortSaving) {
	        sortSaveQueued = true;
	        return;
	      }
	      if (!sortDirty) return;
	      const payload = sortPayload();
	      const payloadText = JSON.stringify(payload);
	      sortSaving = true;
	      saveSortButton.disabled = true;
	      try {
	        const response = await fetch('/api/services/sort', {
	          method: 'PUT',
	          headers: { 'Content-Type': 'application/json' },
	          body: payloadText
	        });
	        if (!response.ok) {
	          const error = await response.json().catch(() => ({ error: '保存排序失败' }));
	          showToast(error.error || '保存排序失败');
	          setSortDirty(true);
	          return;
	        }
	        if (JSON.stringify(sortPayload()) === payloadText) {
	          setSortDirty(false);
	          if (!options.quiet) showToast('排序已保存');
	        } else {
	          setSortDirty(true);
	          sortSaveQueued = true;
	        }
	      } finally {
	        sortSaving = false;
	        saveSortButton.disabled = !sortDirty;
	      }
	      if (sortSaveQueued) {
	        sortSaveQueued = false;
	        if (sortDirty) saveSort(options);
	      }
	    }
    async function uploadIcon(file) {
      if (!file) return;
      const formData = new FormData();
      formData.append('file', file);
      formData.append('asset_type', 'icon');
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
      formData.append('asset_type', 'wallpaper');
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
        applyBackgroundPreview(settingField('background_color').value, settingField('background_image').value, settingField('background_overlay').value, settingsPreview);
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
	    function clearSortAnimation(node, animation) {
	      if (sortAnimations.get(node) !== animation) return;
	      sortAnimations.delete(node);
	      node.style.willChange = '';
	    }
	    function animateGridMove(mutator) {
	      const before = captureSortRects();
	      mutator();
	      const moved = [];
	      for (const node of animatedSortNodes()) {
	        const first = before.get(node);
	        if (!first) continue;
	        const existing = sortAnimations.get(node);
	        if (existing) {
	          existing.cancel();
	          sortAnimations.delete(node);
	        }
	        node.style.transition = '';
	        node.style.transform = '';
	        const last = layoutSortRect(node);
	        const dx = first.left - last.left;
	        const dy = first.top - last.top;
	        if (Math.abs(dx) < 1 && Math.abs(dy) < 1) continue;
	        moved.push({ node, dx: Math.round(dx), dy: Math.round(dy) });
	      }
	      if (!moved.length) return;
	      for (const { node, dx, dy } of moved) {
	        if (typeof node.animate !== 'function') {
	          node.style.transition = 'transform 220ms cubic-bezier(.16,1,.3,1)';
	          node.style.transform = 'translate3d(' + dx + 'px,' + dy + 'px,0)';
	          node.getBoundingClientRect();
	          window.requestAnimationFrame(() => { node.style.transform = ''; });
	          window.setTimeout(() => { node.style.transition = ''; }, 260);
	          continue;
	        }
	        node.style.willChange = 'transform';
	        const animation = node.animate([
	          { transform: 'translate3d(' + dx + 'px,' + dy + 'px,0)' },
	          { transform: 'translate3d(0,0,0)' }
	        ], { duration: 240, easing: 'cubic-bezier(.16,1,.3,1)', fill: 'none' });
	        sortAnimations.set(node, animation);
	        animation.onfinish = () => clearSortAnimation(node, animation);
	        animation.oncancel = () => clearSortAnimation(node, animation);
	      }
	    }
	    function layoutSortRect(node) {
	      const parent = node.offsetParent;
	      if (!parent) return node.getBoundingClientRect();
	      const parentRect = parent.getBoundingClientRect();
	      const left = parentRect.left + node.offsetLeft;
	      const top = parentRect.top + node.offsetTop;
	      const width = node.offsetWidth;
	      const height = node.offsetHeight;
	      return { left, top, right: left + width, bottom: top + height, width, height };
	    }
	    function sortRowsFor(items) {
	      const rows = [];
	      for (const item of items) {
	        const rect = layoutSortRect(item);
	        let row = rows.find(existing => Math.abs(existing.top - rect.top) < 8);
	        if (!row) {
	          row = { top: rect.top, bottom: rect.bottom, items: [] };
	          rows.push(row);
	        }
	        row.top = Math.min(row.top, rect.top);
	        row.bottom = Math.max(row.bottom, rect.bottom);
	        row.items.push({ item, rect });
	      }
	      rows.sort((a, b) => a.top - b.top);
	      for (const row of rows) row.items.sort((a, b) => a.rect.left - b.rect.left);
	      return rows;
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
	        if (JSON.stringify(sortPayload()) !== state.startOrder) {
	          setSortDirty(true);
	          saveSort();
	        }
	        updateGroupCounts();
	      }
	    }
	    function placeDraggedItem(clientX, clientY) {
	      if (!dragState?.dragging) return;
	      const target = document.elementFromPoint(clientX, clientY);
	      if (!target) return;
	      const targetItem = target.closest('.app-icon:not(.is-dragging)');
	      const targetGrid = targetItem?.closest('.icon-grid') || target.closest('.icon-grid');
	      if (!targetGrid) return;
	      const items = [...targetGrid.querySelectorAll('.app-icon:not(.is-dragging)')];
	      if (!items.length) {
	        appendPlaceholderTo(targetGrid);
	        return;
	      }
	      const rows = sortRowsFor(items);
	      const row = rows.find(candidate => clientY <= candidate.bottom) || rows[rows.length - 1];
	      for (const { item, rect } of row.items) {
	        const centerX = rect.left + rect.width / 2;
	        if (clientX < centerX) {
	          movePlaceholderBefore(item);
	          return;
	        }
	      }
	      movePlaceholderAfter(row.items[row.items.length - 1].item);
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
          return;
        }
        if (event.button === 0 && !event.metaKey && !event.ctrlKey && !event.shiftKey && !event.altKey) {
          event.preventDefault();
          openEntryURL(preferredURL(item, accessMode));
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
	      if (actionButton.dataset.action === 'manage-groups') openGroups();
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
      if (action === 'open-external') { const url = itemURL('external'); url ? openEntryURL(url) : showToast('没有外网入口'); }
      if (action === 'open-internal') { const url = itemURL('internal'); url ? openEntryURL(url) : showToast('没有内网入口'); }
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
	    saveSortButton.addEventListener('click', () => saveSort());
    document.querySelector('#open-groups-button').addEventListener('click', openGroups);
    document.querySelector('#groups-close').addEventListener('click', closeGroups);
    document.querySelector('#add-group-button').addEventListener('click', () => openGroupForm(null));
    document.querySelector('#cancel-group-button').addEventListener('click', closeGroupForm);
    saveGroupSortButton.addEventListener('click', saveGroupSort);
    groupForm.addEventListener('submit', saveGroup);
    groupList.addEventListener('click', event => {
      const button = event.target.closest('button[data-action]');
      if (!button) return;
      const row = button.closest('.group-row');
      if (button.dataset.action === 'move-group-up') moveGroup(row, -1);
      if (button.dataset.action === 'move-group-down') moveGroup(row, 1);
      if (button.dataset.action === 'edit-group') openGroupForm(row);
      if (button.dataset.action === 'delete-group') openDeleteConfirm(row?.dataset.groupId, row?.dataset.groupName, 'group');
    });
    document.querySelector('#open-settings-button').addEventListener('click', openSettings);
    document.querySelector('#open-gallery-button').addEventListener('click', () => openGallery('browse'));
    openIconGalleryButton.addEventListener('click', () => openGallery('icon'));
    openBackgroundGalleryButton.addEventListener('click', () => openGallery('background'));
    document.querySelector('#gallery-close').addEventListener('click', closeGallery);
    document.querySelector('#edit-close').addEventListener('click', closeEdit);
    document.querySelector('#settings-close').addEventListener('click', closeSettings);
    backdrop.addEventListener('click', event => { if (event.target === backdrop) closeEdit(); });
    settingsBackdrop.addEventListener('click', event => { if (event.target === settingsBackdrop) closeSettings(); });
    galleryBackdrop.addEventListener('click', event => { if (event.target === galleryBackdrop) closeGallery(); });
    groupsBackdrop.addEventListener('click', event => { if (event.target === groupsBackdrop) closeGroups(); });
    deleteConfirmBackdrop.addEventListener('click', event => { if (event.target === deleteConfirmBackdrop) closeDeleteConfirm(); });
    cancelDeleteButton.addEventListener('click', closeDeleteConfirm);
    confirmDeleteButton.addEventListener('click', performDelete);
    document.addEventListener('keydown', event => { if (event.key === 'Escape' && deleteConfirmBackdrop.classList.contains('is-open')) closeDeleteConfirm(); });
    form.addEventListener('input', refreshPreview);
    form.addEventListener('submit', saveItem);
    deleteButton.addEventListener('click', deleteItem);
    settingsForm.addEventListener('submit', saveSettings);
    settingsForm.addEventListener('input', () => applyBackgroundPreview(settingField('background_color').value, settingField('background_image').value, settingField('background_overlay').value, settingsPreview));
    settingField('background_color_picker').addEventListener('input', () => {
      settingField('background_color').value = settingField('background_color_picker').value;
      applyBackgroundPreview(settingField('background_color').value, settingField('background_image').value, settingField('background_overlay').value, settingsPreview);
    });
    settingField('background_color').addEventListener('input', () => {
      const value = settingField('background_color').value;
      if (/^#[0-9a-fA-F]{6}$/.test(value)) settingField('background_color_picker').value = value;
    });
    document.querySelector('.gallery-tabs').addEventListener('click', event => {
      const tab = event.target.closest('[data-gallery-filter]');
      if (tab) setGalleryFilter(tab.dataset.galleryFilter);
    });
    galleryGrid.addEventListener('click', async event => {
      const button = event.target.closest('button[data-action]');
      if (!button) return;
      const card = button.closest('.gallery-card');
      const asset = galleryAssets.find(item => item.url === card?.dataset.url);
      if (!asset) return;
      if (button.dataset.action === 'copy-asset') await copyText(asset.url);
      if (button.dataset.action === 'use-icon-asset') {
        field('icon').value = asset.url;
        refreshPreview();
        closeGallery();
        showToast('图标路径已填入');
      }
      if (button.dataset.action === 'use-background-asset') await setBackgroundImageFromAsset(asset);
      if (button.dataset.action === 'delete-asset') openDeleteConfirm(asset.url, asset.name || asset.url, 'asset');
    });
    document.querySelector('#reset-background-button').addEventListener('click', () => {
      settingField('background_color').value = '#000000';
      settingField('background_color_picker').value = '#000000';
      settingField('background_image').value = '';
      settingField('background_overlay').value = 'medium';
      applyBackgroundPreview('#000000', '', 'medium', settingsPreview);
    });
    uploadButton.addEventListener('click', () => uploadFile.click());
    uploadFile.addEventListener('change', () => uploadIcon(uploadFile.files?.[0]));
    uploadBackgroundButton.addEventListener('click', () => uploadBackgroundFile.click());
    uploadBackgroundFile.addEventListener('change', () => uploadBackground(uploadBackgroundFile.files?.[0]));
    for (const button of galleryUploadButtons) {
      button.addEventListener('click', () => {
        galleryPendingUploadType = button.dataset.uploadType === 'wallpaper' ? 'wallpaper' : 'icon';
        galleryUploadFile.click();
      });
    }
    galleryUploadFile.addEventListener('change', () => uploadGalleryAsset(galleryUploadFile.files?.[0], galleryPendingUploadType));
    setAccessMode(savedAccessMode(), false);
    updateGroupCounts(); refreshStatus(); setInterval(refreshStatus, 30000);
  </script>
</body>
</html>`

const faviconSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">
  <rect width="64" height="64" rx="14" fill="#141414"/>
  <path d="M17 20h30v24H17z" fill="none" stroke="#f7f7f7" stroke-width="5" stroke-linejoin="round"/>
  <path d="M24 27h4v10h-4zm8 0h4v10h-4zm8 0h4v10h-4z" fill="#67e0b6"/>
</svg>`

const setupTemplate = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>首次设置 - {{.Title}}</title>
  <style>
    :root {
      --bg: #050505;
      --surface: rgba(24,24,27,.82);
      --field: rgba(255,255,255,.08);
      --line: rgba(255,255,255,.13);
      --text: #f7f7f8;
      --muted: #b9bac3;
      --accent: #67e0b6;
      --accent-strong: #8ff0cd;
      --bad: #ff8b8b;
    }
    * { box-sizing: border-box; }
    html { min-height: 100%; background: var(--bg); color: var(--text); font-family: Inter, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; }
    body {
      min-height: 100vh;
      margin: 0;
      display: grid;
      place-items: center;
      padding: 32px 18px;
      background: linear-gradient(135deg, #050505, #17171b 54%, #050505);
    }
    main {
      width: min(430px, 100%);
      display: grid;
      gap: 18px;
    }
    .subtitle {
      margin: 0;
      color: var(--muted);
      line-height: 1.65;
      text-align: center;
    }
    form, .blocked {
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
    .password-field {
      position: relative;
      display: grid;
    }
    .password-field input {
      padding-right: 52px;
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
    .inline-icon { display: inline-grid; place-items: center; width: 1em; height: 1em; line-height: 1; }
    .inline-icon svg { display: block; width: 1em; height: 1em; }
    .inline-icon-fallback { font-weight: 900; }
    .password-toggle {
      position: absolute;
      top: 50%;
      right: 7px;
      width: 38px;
      height: 38px;
      min-height: 0;
      transform: translateY(-50%);
      border: 0;
      border-radius: 8px;
      background: transparent;
      color: var(--muted);
      display: grid;
      place-items: center;
      padding: 0;
      cursor: pointer;
    }
    .password-toggle:hover {
      background: rgba(255,255,255,.08);
      color: var(--text);
    }
    .password-toggle .inline-icon { font-size: 22px; }
    .error {
      margin: 0;
      color: var(--bad);
      font-size: 14px;
      line-height: 1.5;
    }
    @media (max-width: 760px) {
      body { align-items: start; padding-top: 96px; }
      form, .blocked { padding: 20px; }
    }
  </style>
</head>
<body>
  <main>
    <p class="subtitle">首次使用，请设置管理员账号。</p>
    {{if .Allowed}}
    <form method="post" action="/setup">
      {{if .Error}}<p class="error">{{.Error}}</p>{{end}}
      <label>账号
        <input name="username" type="text" autocomplete="username" value="admin" autofocus required>
      </label>
      <label>密码
        <span class="password-field">
          <input id="setup-password" name="password" type="password" autocomplete="new-password" minlength="8" required>
          <button class="password-toggle" type="button" data-password-toggle data-target="setup-password" aria-label="显示密码" title="显示密码">{{icon "mdi:eye"}}</button>
        </span>
      </label>
      <label>确认密码
        <span class="password-field">
          <input id="setup-confirm-password" name="confirm_password" type="password" autocomplete="new-password" minlength="8" required>
          <button class="password-toggle" type="button" data-password-toggle data-target="setup-confirm-password" aria-label="显示密码" title="显示密码">{{icon "mdi:eye"}}</button>
        </span>
      </label>
      <button type="submit">完成设置</button>
    </form>
    {{else}}
    <section class="blocked">
      {{if .Error}}<p class="error">{{.Error}}</p>{{else}}<p class="error">当前访问来源不是局域网或本机，不能执行首次设置。请先通过局域网地址访问，或在配置文件里手动设置管理员密码。</p>{{end}}
    </section>
    {{end}}
  </main>
  <script>
    const passwordHiddenIcon = {{iconJSON "mdi:eye"}};
    const passwordVisibleIcon = {{iconJSON "mdi:eye-off"}};
    for (const button of document.querySelectorAll('[data-password-toggle]')) {
      const input = document.getElementById(button.dataset.target);
      if (!input) continue;
      button.addEventListener('click', () => {
        const visible = input.type === 'password';
        input.type = visible ? 'text' : 'password';
        const label = visible ? '隐藏密码' : '显示密码';
        button.setAttribute('aria-label', label);
        button.title = label;
        button.innerHTML = visible ? passwordVisibleIcon : passwordHiddenIcon;
      });
    }
  </script>
</body>
</html>`

const loginTemplate = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>登录 - {{.Title}}</title>
  <link rel="icon" href="/favicon.svg" type="image/svg+xml">
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
    .tool-button .inline-icon { font-size: 22px; }
    .inline-icon { display: inline-grid; place-items: center; width: 1em; height: 1em; line-height: 1; }
    .inline-icon svg { display: block; width: 1em; height: 1em; }
    .inline-icon-fallback { font-weight: 900; }
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
    .password-field {
      position: relative;
      display: grid;
    }
    .password-field input {
      padding-right: 52px;
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
    .password-toggle {
      position: absolute;
      top: 50%;
      right: 7px;
      width: 38px;
      height: 38px;
      min-height: 0;
      transform: translateY(-50%);
      border: 0;
      border-radius: 8px;
      background: transparent;
      color: var(--muted);
      display: grid;
      place-items: center;
      padding: 0;
      cursor: pointer;
    }
    .password-toggle:hover {
      background: rgba(255,255,255,.08);
      color: var(--text);
    }
    .password-toggle .inline-icon { font-size: 22px; }
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
    <button class="tool-button" type="button" id="access-mode-button" title="当前使用外网入口"><span id="access-mode-icon">{{icon "mdi:web"}}</span></button>
  </div>
  <main>
    <form method="post" action="/login?return_to={{.ReturnTo}}">
      {{if .Error}}<p class="error">{{.Error}}</p>{{end}}
      <label>账号
        <input name="username" type="text" autocomplete="username" autofocus required>
      </label>
      <label>密码
        <span class="password-field">
          <input id="login-password" name="password" type="password" autocomplete="current-password" required>
          <button class="password-toggle" type="button" data-password-toggle data-target="login-password" aria-label="显示密码" title="显示密码">{{icon "mdi:eye"}}</button>
        </span>
      </label>
      <button type="submit">登录</button>
    </form>
  </main>
  <script>
    const accessModeKey = 'home-nav.access-mode';
    const accessModeButton = document.querySelector('#access-mode-button');
    const accessModeIcon = document.querySelector('#access-mode-icon');
    const passwordHiddenIcon = {{iconJSON "mdi:eye"}};
    const passwordVisibleIcon = {{iconJSON "mdi:eye-off"}};
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
      accessModeIcon.innerHTML = accessMode === 'internal' ? {{iconJSON "mdi:lan"}} : {{iconJSON "mdi:web"}};
      try { localStorage.setItem(accessModeKey, accessMode); } catch (_) {}
    }
    for (const button of document.querySelectorAll('[data-password-toggle]')) {
      const input = document.getElementById(button.dataset.target);
      if (!input) continue;
      button.addEventListener('click', () => {
        const visible = input.type === 'password';
        input.type = visible ? 'text' : 'password';
        const label = visible ? '隐藏密码' : '显示密码';
        button.setAttribute('aria-label', label);
        button.title = label;
        button.innerHTML = visible ? passwordVisibleIcon : passwordHiddenIcon;
      });
    }
    accessModeButton.addEventListener('click', () => setAccessMode(document.body.dataset.accessMode === 'internal' ? 'external' : 'internal'));
    setAccessMode(savedAccessMode());
  </script>
</body>
</html>`
