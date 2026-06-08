package server

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
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
	Title    string
	Subtitle string
	Groups   []Group
	Tags     []string
	Auth     AuthConfig
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
	s.mux.HandleFunc("/login", s.handleLogin)
	s.mux.HandleFunc("/logout", s.handleLogout)
	s.mux.HandleFunc("/api/status", s.handleStatus)
	s.mux.HandleFunc("/api/services/", s.handleService)
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

func (s *Server) handleService(w http.ResponseWriter, r *http.Request) {
	if !s.authenticated(r) {
		writeJSONError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	if r.Method != http.MethodPut {
		writeJSONError(w, http.StatusMethodNotAllowed, "请求方法不支持")
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/api/services/")
	if id == "" || strings.Contains(id, "/") {
		writeJSONError(w, http.StatusNotFound, "服务不存在")
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
		Title:    cfg.Title,
		Subtitle: cfg.Subtitle,
		Groups:   cfg.Groups,
		Tags:     collectTags(cfg.Groups),
		Auth:     cfg.Auth,
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
    body { margin: 0; min-height: 100vh; background: var(--bg); color: var(--text); }
    a { color: inherit; text-decoration: none; }
    button, input, textarea, select { font: inherit; }
    .shell { width: min(1240px, calc(100vw - 36px)); margin: 0 auto; padding: 52px 0 80px; }
    .top-tools { position: fixed; top: 22px; right: 22px; display: flex; gap: 10px; z-index: 20; }
    .tool-button { width: 48px; height: 48px; border: 0; border-radius: 8px; background: #141414; color: #fff; display: grid; place-items: center; cursor: pointer; }
    .hero { display: grid; justify-items: center; gap: 22px; margin-bottom: 52px; }
    .title-row { display: flex; align-items: flex-end; justify-content: center; gap: 14px; flex-wrap: wrap; }
    h1 { margin: 0; font-size: clamp(44px, 6vw, 72px); line-height: .95; letter-spacing: 0; font-weight: 800; }
    .clock { display: grid; gap: 4px; padding-bottom: 6px; }
    .clock-time { font-size: clamp(24px, 3vw, 36px); line-height: 1; font-weight: 800; }
    .clock-date { color: var(--muted); font-size: 16px; }
    .search-wrap { width: min(806px, 100%); height: 50px; border: 1px solid #b8bcc6; border-radius: 18px; display: grid; grid-template-columns: 42px 1fr 44px; align-items: center; padding: 0 12px; }
    .search-wrap iconify-icon { color: #fff; font-size: 24px; }
    .search { width: 100%; min-width: 0; border: 0; outline: 0; background: transparent; color: var(--text); font-size: 18px; }
    .search::placeholder { color: #9ea4b0; }
    .groups { display: grid; gap: 56px; }
    .group { display: grid; gap: 24px; }
    .group-title { display: flex; align-items: baseline; gap: 12px; }
    h2 { margin: 0; font-size: 24px; letter-spacing: 0; font-weight: 800; }
    .group-count { color: var(--muted); font-size: 14px; }
    .icon-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(86px, 1fr)); gap: 28px 20px; align-items: start; }
    .app-icon { display: grid; justify-items: center; gap: 8px; min-width: 0; color: #fff; }
    .icon-button { width: 76px; height: 76px; border: 0; border-radius: 14px; background: var(--panel); color: #fff; display: grid; place-items: center; cursor: pointer; transition: transform .12s ease, background .12s ease; position: relative; }
    .icon-button:hover { transform: translateY(-2px); background: #1f1f1f; }
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
    .form-actions { display: flex; justify-content: flex-end; gap: 14px; margin-top: 30px; }
    .save-button, .delete-button { min-width: 118px; min-height: 58px; border: 0; border-radius: 7px; font-size: 24px; cursor: pointer; }
    .save-button { background: var(--accent); color: #050505; }
    .delete-button { background: var(--danger); color: #050505; display: none; }
    .toast { position: fixed; left: 50%; bottom: 28px; transform: translateX(-50%); background: #202026; color: #fff; border: 1px solid #555761; border-radius: 999px; padding: 10px 18px; display: none; z-index: 80; }
    .toast.is-open { display: block; }
    @media (max-width: 760px) {
      .shell { width: min(100vw - 24px, 1240px); padding-top: 26px; }
      .top-tools { top: 12px; right: 12px; }
      .hero { margin-bottom: 34px; align-items: start; justify-items: stretch; }
      .title-row { justify-content: flex-start; align-items: flex-start; }
      h1 { font-size: 44px; }
      .clock { padding-bottom: 0; }
      .search-wrap { border-radius: 14px; }
      .icon-grid { grid-template-columns: repeat(auto-fill, minmax(74px, 1fr)); gap: 22px 14px; }
      .icon-button { width: 66px; height: 66px; border-radius: 13px; }
      .icon-button iconify-icon { font-size: 36px; }
      .app-name { width: 78px; font-size: 13px; }
      .menu { left: 12px !important; right: 12px; top: auto !important; bottom: 12px; width: auto; }
      .modal { border-radius: 18px; padding: 22px 18px 24px; }
      .preview { grid-template-columns: 1fr; padding: 16px; }
      .preview-wide { justify-self: stretch; width: 100%; height: 110px; font-size: 22px; }
      .preview-square { width: 100%; }
      .form-grid { grid-template-columns: 1fr; }
      .field.full { grid-column: auto; }
      .field label { font-size: 17px; }
      .field input, .field textarea, .field select { min-height: 50px; font-size: 16px; }
      .form-actions { justify-content: stretch; }
      .save-button { width: 100%; }
    }
  </style>
</head>
<body>
  {{if .Auth.Enabled}}<form class="top-tools" method="post" action="/logout"><button class="tool-button" type="submit" title="退出登录"><iconify-icon icon="mdi:logout"></iconify-icon></button></form>{{end}}
  <main class="shell">
    <section class="hero">
      <div class="title-row">
        <h1>{{.Title}}</h1>
        <div class="clock"><div class="clock-time" id="clock-time">--:--:--</div><div class="clock-date" id="clock-date">--</div></div>
      </div>
      <div class="search-wrap"><iconify-icon icon="logos:google-icon"></iconify-icon><input id="search" class="search" type="search" placeholder="搜索服务、描述或标签" autocomplete="off"><iconify-icon icon="mdi:magnify"></iconify-icon></div>
    </section>

    <section class="groups">
      {{range .Groups}}
      <section class="group" data-group-id="{{.ID}}">
        <div class="group-title"><h2>{{.Name}}</h2><span class="group-count"><span class="group-visible-count">0</span> / {{len .Services}}</span></div>
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
          <div class="field"><label>在线图标名或图片 URL <a class="field-link" href="https://icon-sets.iconify.design/" target="_blank" rel="noreferrer">在线图标库</a></label><input name="icon" placeholder="mdi:nas"></div>
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
        <div class="form-actions"><button class="save-button" type="submit">保存</button></div>
      </form>
    </section>
  </div>
  <div class="toast" id="toast"></div>

  <script>
    const searchInput = document.querySelector('#search');
    const items = [...document.querySelectorAll('.app-icon')];
    const groups = [...document.querySelectorAll('.group')];
    const menu = document.querySelector('#item-menu');
    const backdrop = document.querySelector('#edit-backdrop');
    const form = document.querySelector('#edit-form');
    const toast = document.querySelector('#toast');
    const statusLabels = { healthy: '正常', unhealthy: '异常', unknown: '未知', disabled: '未启用' };
    let activeItem = null;

    function updateClock() {
      const now = new Date();
      document.querySelector('#clock-time').textContent = now.toLocaleTimeString('en-GB', { hour12: false });
      document.querySelector('#clock-date').textContent = now.toLocaleDateString('en-US', { month: 'numeric', day: 'numeric', weekday: 'long' });
    }
    function normalize(value) { return (value || '').trim().toLowerCase(); }
    function showToast(message) { toast.textContent = message; toast.classList.add('is-open'); setTimeout(() => toast.classList.remove('is-open'), 1800); }
    function itemURL(type) { return type === 'internal' ? activeItem?.dataset.internalUrl : activeItem?.dataset.externalUrl; }
    function onlineIconSrc(icon) {
      const parts = String(icon || '').split(':');
      if (parts.length !== 2 || !parts[0] || !parts[1]) return '';
      return '/.iconify/' + encodeURIComponent(parts[0]) + '/' + encodeURIComponent(parts[1]) + '.svg';
    }
    function iconMarkup(item) {
      const icon = item.dataset.iconValue || '';
      const text = item.dataset.iconText || (item.dataset.name || '?').slice(0, 3).toUpperCase();
      if (icon.startsWith('http://') || icon.startsWith('https://') || icon.startsWith('/')) return '<img src="' + escapeHTML(icon) + '" alt="">';
      if (icon.includes(':')) return '<img src="' + escapeHTML(onlineIconSrc(icon)) + '" alt="">';
      return '<span class="icon-fallback">' + escapeHTML(text) + '</span>';
    }
    function escapeHTML(value) { return String(value || '').replace(/[&<>"]/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c])); }
    function applyFilters() {
      const keyword = normalize(searchInput.value);
      let visibleTotal = 0;
      for (const item of items) {
        const visible = !keyword || normalize(item.dataset.search).includes(keyword);
        item.classList.toggle('is-hidden', !visible);
        if (visible) visibleTotal += 1;
      }
      for (const group of groups) {
        const count = group.querySelectorAll('.app-icon:not(.is-hidden)').length;
        group.classList.toggle('is-hidden', count === 0);
        group.querySelector('.group-visible-count').textContent = String(count);
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
    function openEdit(item) {
      activeItem = item;
      form.id.value = item.dataset.serviceId;
      form.name.value = item.dataset.name || '';
      form.description.value = item.dataset.description || '';
      form.icon_text.value = item.dataset.iconText || '';
      form.icon.value = item.dataset.iconValue || '';
      form.external_url.value = item.dataset.externalUrl || '';
      form.internal_url.value = item.dataset.internalUrl || '';
      form.group_id.value = item.dataset.groupId || '';
      form.tags.value = item.dataset.tags || '';
      form.notes.value = item.dataset.notes || '';
      form.health_type.value = item.dataset.healthType || 'disabled';
      form.health_url.value = item.dataset.healthUrl || '';
      form.health_address.value = item.dataset.healthAddress || '';
      form.health_expect_status.value = item.dataset.healthExpectStatus === '0' ? '' : (item.dataset.healthExpectStatus || '');
      form.health_timeout.value = item.dataset.healthTimeout || '2s';
      refreshPreview();
      backdrop.classList.add('is-open');
      closeMenu();
    }
    function closeEdit() { backdrop.classList.remove('is-open'); }
    function refreshPreview() {
      const mock = { dataset: { iconValue: form.icon.value, iconText: form.icon_text.value, name: form.name.value } };
      document.querySelector('#preview-wide-icon').innerHTML = iconMarkup(mock);
      document.querySelector('#preview-square-icon').innerHTML = iconMarkup(mock);
      document.querySelector('#preview-wide-name').textContent = form.name.value || '-';
      document.querySelector('#preview-square-name').textContent = form.name.value || '-';
    }
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
      const id = form.id.value;
      const expectStatus = Number(form.health_expect_status.value || 0);
      const payload = {
        name: form.name.value,
        description: form.description.value,
        icon_text: form.icon_text.value,
        icon: form.icon.value,
        external_url: form.external_url.value,
        internal_url: form.internal_url.value,
        group_id: form.group_id.value,
        tags: form.tags.value.split(',').map(v => v.trim()).filter(Boolean),
        notes: form.notes.value,
        health: {
          type: form.health_type.value,
          url: form.health_url.value,
          address: form.health_address.value,
          expect_status: expectStatus,
          timeout: form.health_timeout.value || '2s'
        }
      };
      const response = await fetch('/api/services/' + encodeURIComponent(id), { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload) });
      if (!response.ok) {
        const error = await response.json().catch(() => ({ error: '保存失败' }));
        showToast(error.error || '保存失败');
        return;
      }
      showToast('已保存');
      setTimeout(() => location.reload(), 500);
    }

    for (const item of items) {
      const button = item.querySelector('.icon-button');
      let longPressTimer = null;
      let suppressClick = false;
      button.addEventListener('click', event => {
        if (suppressClick) {
          suppressClick = false;
          event.preventDefault();
          return;
        }
      });
      button.addEventListener('contextmenu', event => {
        event.preventDefault();
        openMenuAt(item, event.clientX, event.clientY);
      });
      button.addEventListener('pointerdown', event => {
        if (event.pointerType === 'mouse') return;
        window.clearTimeout(longPressTimer);
        longPressTimer = window.setTimeout(() => {
          suppressClick = true;
          openMenuNear(item, button);
        }, 520);
      });
      for (const eventName of ['pointerup', 'pointercancel', 'pointerleave']) {
        button.addEventListener(eventName, () => window.clearTimeout(longPressTimer));
      }
    }
    menu.addEventListener('click', event => {
      const button = event.target.closest('button[data-action]');
      if (!button || !activeItem) return;
      const action = button.dataset.action;
      if (action === 'open-external') { const url = itemURL('external'); url ? window.open(url, '_blank', 'noreferrer') : showToast('没有外网入口'); }
      if (action === 'open-internal') { const url = itemURL('internal'); url ? window.open(url, '_blank', 'noreferrer') : showToast('没有内网入口'); }
      if (action === 'copy-external') copyText(itemURL('external'));
      if (action === 'copy-internal') copyText(itemURL('internal'));
      if (action === 'edit') openEdit(activeItem);
    });
    document.addEventListener('click', event => { if (!menu.contains(event.target) && !event.target.closest('.icon-button')) closeMenu(); });
    document.querySelector('#edit-close').addEventListener('click', closeEdit);
    backdrop.addEventListener('click', event => { if (event.target === backdrop) closeEdit(); });
    form.addEventListener('input', refreshPreview);
    form.addEventListener('submit', saveItem);
    searchInput.addEventListener('input', applyFilters);
    updateClock(); setInterval(updateClock, 1000);
    applyFilters(); refreshStatus(); setInterval(refreshStatus, 30000);
  </script>
</body>
</html>`

const loginTemplate = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>登录 - {{.Title}}</title>
  <style>
    :root {
      color-scheme: light;
      --bg: #f4f6f8;
      --surface: #ffffff;
      --text: #172033;
      --muted: #637083;
      --line: #d8e0ea;
      --accent: #1666c5;
      --accent-strong: #0f4f9f;
      --bad: #b42318;
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
    main {
      width: min(420px, 100%);
      display: grid;
      gap: 18px;
    }
    h1 {
      margin: 0;
      font-size: 30px;
      line-height: 1.12;
      letter-spacing: 0;
    }
    .subtitle {
      margin: 8px 0 0;
      color: var(--muted);
      line-height: 1.6;
    }
    form {
      display: grid;
      gap: 14px;
      padding: 20px;
      border: 1px solid var(--line);
      border-radius: 8px;
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
      min-height: 42px;
      border: 1px solid var(--line);
      border-radius: 8px;
      padding: 0 12px;
      color: var(--text);
      background: #fff;
      font-size: 15px;
      outline: none;
    }
    input:focus {
      border-color: var(--accent);
      box-shadow: 0 0 0 3px rgba(22, 102, 197, .14);
    }
    button {
      min-height: 42px;
      border: 1px solid var(--accent);
      border-radius: 8px;
      background: var(--accent);
      color: #fff;
      font-size: 15px;
      cursor: pointer;
    }
    button:hover { background: var(--accent-strong); }
    .error {
      margin: 0;
      color: var(--bad);
      font-size: 14px;
      line-height: 1.5;
    }
  </style>
</head>
<body>
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
</body>
</html>`
