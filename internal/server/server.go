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
	Title          string
	Subtitle       string
	Groups         []Group
	Tags           []string
	Auth           AuthConfig
	Appearance     Appearance
	BackgroundCSS  template.CSS
	NavigationJSON template.JS
	AssetVersion   string
	EmptyService   Service
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
	Name              string                     `json:"name"`
	Description       string                     `json:"description"`
	IconText          string                     `json:"icon_text"`
	Icon              string                     `json:"icon"`
	InternalURL       string                     `json:"internal_url"`
	InternalDomainURL string                     `json:"internal_domain_url"`
	ExternalURL       string                     `json:"external_url"`
	Tags              []string                   `json:"tags"`
	Notes             string                     `json:"notes"`
	GroupID           string                     `json:"group_id"`
	Pinned            *bool                      `json:"pinned,omitempty"`
	Health            ServiceHealthUpdateRequest `json:"health"`
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
		"iconSetJSON": iconSetJSON,
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
	s.mux.HandleFunc("/api/navigation", s.handleNavigation)
	s.mux.HandleFunc("/assets/app.css", handleUIAsset)
	s.mux.HandleFunc("/assets/app.js", handleUIAsset)
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
	_ = json.NewEncoder(w).Encode(map[string]any{"group": group, "navigation": navigationState(cfg)})
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
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "navigation": navigationState(cfg)})
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
	_ = json.NewEncoder(w).Encode(map[string]any{"service": service, "navigation": navigationState(cfg)})
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
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "navigation": navigationState(cfg)})
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
		_ = json.NewEncoder(w).Encode(map[string]any{"deleted": id, "navigation": navigationState(cfg)})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"group": group, "navigation": navigationState(cfg)})
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
		_ = json.NewEncoder(w).Encode(map[string]any{"deleted": id, "navigation": navigationState(cfg)})
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
		"service":    service,
		"navigation": navigationState(cfg),
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
	_ = json.NewEncoder(w).Encode(map[string]any{"appearance": cfg.Appearance, "navigation": navigationState(cfg)})
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
		Title:          cfg.Title,
		Subtitle:       cfg.Subtitle,
		Groups:         cfg.Groups,
		Tags:           collectTags(cfg.Groups),
		Auth:           cfg.Auth,
		Appearance:     cfg.Appearance,
		BackgroundCSS:  backgroundCSS(cfg.Appearance),
		NavigationJSON: navigationJSON(cfg),
		AssetVersion:   uiAssetVersion,
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
		ID:                uniqueServiceID(cfg, payload.Name),
		Name:              payload.Name,
		Description:       payload.Description,
		IconText:          payload.IconText,
		Icon:              payload.Icon,
		InternalURL:       payload.InternalURL,
		InternalDomainURL: payload.InternalDomainURL,
		ExternalURL:       payload.ExternalURL,
		Tags:              payload.Tags,
		Notes:             payload.Notes,
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
	if payload.Pinned != nil {
		service.Pinned = *payload.Pinned
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
	service.InternalDomainURL = payload.InternalDomainURL
	service.ExternalURL = payload.ExternalURL
	service.Tags = payload.Tags
	service.Notes = payload.Notes
	if payload.Pinned != nil {
		service.Pinned = *payload.Pinned
	}
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
	message, field := userError(message)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message, "field": field})
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
	if html := bundledIcon(icon); html != "" {
		return html
	}
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
		if _, bundled := uiIconShapes[icon]; !bundled {
			seen[icon] = struct{}{}
		}
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
    .access-mode-control { position: relative; }
    .access-mode-trigger { width: 142px; height: 48px; padding: 0 15px 0 14px; display: flex; align-items: center; justify-content: space-between; gap: 8px; border: 1px solid #4c4d56; border-radius: 12px; background: #141414; color: #fff; font-size: 14px; font-weight: 600; white-space: nowrap; cursor: pointer; }
    .access-mode-trigger:hover, .access-mode-trigger[aria-expanded="true"] { border-color: #73777b; background: #242424; }
    .access-mode-trigger:focus-visible, .access-mode-option:focus-visible { outline: 2px solid #67e0b6; outline-offset: 2px; }
    .access-mode-chevron { width: 8px; height: 8px; flex: 0 0 auto; border-right: 2px solid #c8d4ce; border-bottom: 2px solid #c8d4ce; transform: translateY(-2px) rotate(45deg); }
    .access-mode-trigger[aria-expanded="true"] .access-mode-chevron { transform: translateY(2px) rotate(225deg); }
    .access-mode-menu { position: absolute; top: calc(100% + 6px); right: 0; width: 180px; max-width: calc(100vw - 24px); padding: 6px; display: grid; gap: 2px; border: 1px solid #4c4d56; border-radius: 12px; background: #1c211e; box-shadow: 0 14px 36px rgba(0,0,0,.42); }
    .access-mode-menu[hidden] { display: none; }
    .access-mode-option { min-height: 36px; padding: 8px 10px; display: flex; align-items: center; justify-content: space-between; gap: 10px; border: 0; border-radius: 8px; background: transparent; color: #f7f7f7; font-size: 14px; line-height: 1.45; text-align: left; white-space: nowrap; cursor: pointer; }
    .access-mode-option:hover, .access-mode-option:focus-visible { background: #29342f; }
    .access-mode-option[aria-checked="true"] { background: #263b30; font-weight: 600; }
    .access-mode-option[aria-checked="true"]:hover, .access-mode-option[aria-checked="true"]:focus-visible { background: #304d3b; }
    .access-mode-option::after { content: ''; width: 6px; height: 10px; margin: -3px 4px 0 0; flex: 0 0 auto; border-right: 2px solid #67e0b6; border-bottom: 2px solid #67e0b6; transform: rotate(45deg); opacity: 0; }
    .access-mode-option[aria-checked="true"]::after { opacity: 1; }
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
      body.access-mode-menu-open { padding-top: 252px; }
      .top-tools { top: 12px; right: 12px; }
      .access-mode-option { min-height: 44px; }
      form { padding: 20px; }
    }
  </style>
</head>
<body>
  <div class="top-tools">
    <div class="access-mode-control" id="access-mode-control">
      <button class="access-mode-trigger" id="access-mode-button" type="button" aria-label="优先访问入口，当前外网优先" aria-haspopup="menu" aria-expanded="false" aria-controls="access-mode-menu"><span id="access-mode-label">外网优先</span><span class="access-mode-chevron" aria-hidden="true"></span></button>
      <div class="access-mode-menu" id="access-mode-menu" role="menu" aria-label="优先访问入口" hidden>
        <button class="access-mode-option" type="button" role="menuitemradio" data-access-mode="external" aria-checked="true" tabindex="-1">外网优先</button>
        <button class="access-mode-option" type="button" role="menuitemradio" data-access-mode="internal" aria-checked="false" tabindex="-1">内网 IP 优先</button>
        <button class="access-mode-option" type="button" role="menuitemradio" data-access-mode="internal_domain" aria-checked="false" tabindex="-1">内网域名优先</button>
      </div>
    </div>
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
    const accessModeControl = document.querySelector('#access-mode-control');
    const accessModeButton = document.querySelector('#access-mode-button');
    const accessModeLabel = document.querySelector('#access-mode-label');
    const accessModeMenu = document.querySelector('#access-mode-menu');
    const accessModeOptions = [...accessModeMenu.querySelectorAll('[data-access-mode]')];
    const accessModes = ['external', 'internal', 'internal_domain'];
    const passwordHiddenIcon = {{iconJSON "mdi:eye"}};
    const passwordVisibleIcon = {{iconJSON "mdi:eye-off"}};
    function savedAccessMode() {
      try {
        const mode = localStorage.getItem(accessModeKey);
        return accessModes.includes(mode) ? mode : 'external';
      } catch (_) {
        return 'external';
      }
    }
    function setAccessMode(mode) {
      const accessMode = accessModes.includes(mode) ? mode : 'external';
      document.body.dataset.accessMode = accessMode;
      const selectedOption = accessModeOptions.find(option => option.dataset.accessMode === accessMode);
      const label = selectedOption.textContent.trim();
      accessModeLabel.textContent = label;
      accessModeButton.setAttribute('aria-label', '优先访问入口，当前' + label);
      for (const option of accessModeOptions) option.setAttribute('aria-checked', String(option === selectedOption));
      try { localStorage.setItem(accessModeKey, accessMode); } catch (_) {}
    }
    function openAccessModeMenu(index) {
      accessModeMenu.hidden = false;
      accessModeButton.setAttribute('aria-expanded', 'true');
      document.body.classList.add('access-mode-menu-open');
      const selectedIndex = accessModeOptions.findIndex(option => option.dataset.accessMode === document.body.dataset.accessMode);
      accessModeOptions[index ?? selectedIndex].focus();
    }
    function closeAccessModeMenu(restoreFocus) {
      if (accessModeMenu.hidden) return;
      accessModeMenu.hidden = true;
      accessModeButton.setAttribute('aria-expanded', 'false');
      document.body.classList.remove('access-mode-menu-open');
      if (restoreFocus) accessModeButton.focus();
    }
    function bindAccessModeMenu() {
      accessModeButton.addEventListener('click', () => accessModeMenu.hidden ? openAccessModeMenu() : closeAccessModeMenu(true));
      accessModeButton.addEventListener('keydown', event => {
        if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return;
        event.preventDefault();
        openAccessModeMenu(event.key === 'ArrowUp' ? accessModeOptions.length - 1 : 0);
      });
      accessModeMenu.addEventListener('click', event => {
        const option = event.target.closest('[data-access-mode]');
        if (!option) return;
        setAccessMode(option.dataset.accessMode);
        closeAccessModeMenu(true);
      });
      accessModeMenu.addEventListener('keydown', event => {
        if (event.key === 'Tab' && event.shiftKey) {
          event.preventDefault();
          closeAccessModeMenu(true);
          return;
        }
        const index = accessModeOptions.indexOf(document.activeElement);
        let next = index;
        if (event.key === 'ArrowDown') next = (index + 1) % accessModeOptions.length;
        else if (event.key === 'ArrowUp') next = (index - 1 + accessModeOptions.length) % accessModeOptions.length;
        else if (event.key === 'Home') next = 0;
        else if (event.key === 'End') next = accessModeOptions.length - 1;
        else if (event.key === 'Escape') {
          event.preventDefault();
          closeAccessModeMenu(true);
          return;
        } else return;
        event.preventDefault();
        accessModeOptions[next].focus();
      });
      accessModeControl.addEventListener('focusout', event => {
        if (!accessModeControl.contains(event.relatedTarget)) closeAccessModeMenu(false);
      });
      document.addEventListener('pointerdown', event => {
        if (!accessModeControl.contains(event.target)) closeAccessModeMenu(false);
      });
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
    bindAccessModeMenu();
    setAccessMode(savedAccessMode());
  </script>
</body>
</html>`
