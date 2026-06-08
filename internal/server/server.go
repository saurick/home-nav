package server

import (
	"context"
	"encoding/json"
	"html/template"
	"net/http"
	"sort"
)

type Server struct {
	cfg      *Config
	statuses *StatusCache
	tpl      *template.Template
	mux      *http.ServeMux
}

type PageData struct {
	Title    string
	Subtitle string
	Groups   []Group
	Tags     []string
}

func New(configPath string) (*Server, error) {
	cfg, err := LoadConfig(configPath)
	if err != nil {
		return nil, err
	}

	tpl, err := template.New("index").Parse(indexTemplate)
	if err != nil {
		return nil, err
	}

	s := &Server{
		cfg:      cfg,
		statuses: NewStatusCache(cfg),
		tpl:      tpl,
		mux:      http.NewServeMux(),
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
	s.mux.HandleFunc("/api/status", s.handleStatus)
	s.mux.HandleFunc("/", s.handleIndex)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(s.statuses.Snapshot())
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.tpl.Execute(w, PageData{
		Title:    s.cfg.Title,
		Subtitle: s.cfg.Subtitle,
		Groups:   s.cfg.Groups,
		Tags:     collectTags(s.cfg.Groups),
	})
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
  <style>
    :root {
      color-scheme: light;
      --bg: #f4f6f8;
      --surface: #ffffff;
      --surface-soft: #eef3f8;
      --text: #172033;
      --muted: #637083;
      --line: #d8e0ea;
      --accent: #1666c5;
      --accent-strong: #0f4f9f;
      --ok: #16803c;
      --bad: #b42318;
      --unknown: #77622a;
      --disabled: #6b7280;
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", sans-serif;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      min-height: 100vh;
      background: var(--bg);
      color: var(--text);
    }
    a { color: inherit; text-decoration: none; }
    .shell {
      width: min(1180px, calc(100vw - 32px));
      margin: 0 auto;
      padding: 28px 0 48px;
    }
    header {
      display: grid;
      gap: 18px;
      padding: 12px 0 22px;
      border-bottom: 1px solid var(--line);
    }
    .title-row {
      display: flex;
      justify-content: space-between;
      gap: 18px;
      align-items: end;
    }
    h1 {
      margin: 0;
      font-size: clamp(28px, 4vw, 42px);
      line-height: 1.08;
      letter-spacing: 0;
    }
    .subtitle {
      margin: 8px 0 0;
      color: var(--muted);
      line-height: 1.6;
      max-width: 720px;
    }
    .summary {
      color: var(--muted);
      font-size: 14px;
      white-space: nowrap;
    }
    .controls {
      display: grid;
      grid-template-columns: minmax(220px, 420px) 1fr;
      gap: 12px;
      align-items: start;
    }
    .search {
      width: 100%;
      min-height: 42px;
      border: 1px solid var(--line);
      border-radius: 8px;
      padding: 0 14px;
      background: var(--surface);
      color: var(--text);
      font-size: 15px;
      outline: none;
    }
    .search:focus { border-color: var(--accent); box-shadow: 0 0 0 3px rgba(22, 102, 197, .14); }
    .tags {
      display: flex;
      flex-wrap: wrap;
      gap: 8px;
      min-width: 0;
    }
    .tag-filter {
      min-height: 34px;
      border: 1px solid var(--line);
      border-radius: 999px;
      background: var(--surface);
      color: var(--muted);
      padding: 0 12px;
      cursor: pointer;
      font-size: 14px;
    }
    .tag-filter.is-active {
      border-color: var(--accent);
      background: #e8f1fc;
      color: var(--accent-strong);
    }
    .groups {
      display: grid;
      gap: 28px;
      padding-top: 24px;
    }
    .group {
      display: grid;
      gap: 14px;
    }
    .group-title {
      display: flex;
      align-items: baseline;
      gap: 10px;
    }
    h2 {
      margin: 0;
      font-size: 20px;
      letter-spacing: 0;
    }
    .group-count {
      color: var(--muted);
      font-size: 13px;
    }
    .cards {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(250px, 1fr));
      gap: 12px;
    }
    .card {
      display: grid;
      grid-template-rows: auto 1fr auto;
      gap: 12px;
      min-height: 218px;
      padding: 16px;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: var(--surface);
    }
    .card-top {
      display: grid;
      grid-template-columns: 44px 1fr auto;
      gap: 12px;
      align-items: start;
      min-width: 0;
    }
    .icon {
      width: 44px;
      height: 44px;
      display: grid;
      place-items: center;
      border-radius: 8px;
      background: var(--surface-soft);
      color: var(--accent-strong);
      font-weight: 700;
      overflow: hidden;
    }
    .icon img {
      width: 100%;
      height: 100%;
      object-fit: cover;
    }
    .service-heading {
      min-width: 0;
    }
    .service-name {
      margin: 0;
      font-size: 17px;
      line-height: 1.28;
      overflow-wrap: anywhere;
    }
    .description {
      margin: 5px 0 0;
      color: var(--muted);
      font-size: 14px;
      line-height: 1.45;
      overflow-wrap: anywhere;
    }
    .status {
      display: inline-flex;
      align-items: center;
      gap: 6px;
      min-height: 28px;
      padding: 0 9px;
      border-radius: 999px;
      background: var(--surface-soft);
      color: var(--muted);
      font-size: 12px;
      white-space: nowrap;
    }
    .dot {
      width: 8px;
      height: 8px;
      border-radius: 50%;
      background: currentColor;
    }
    .status[data-status="healthy"] { color: var(--ok); background: #eaf7ee; }
    .status[data-status="unhealthy"] { color: var(--bad); background: #fdecec; }
    .status[data-status="unknown"] { color: var(--unknown); background: #fbf2d9; }
    .status[data-status="disabled"] { color: var(--disabled); background: #eef0f3; }
    .card-body {
      display: grid;
      gap: 10px;
      align-content: start;
      min-width: 0;
    }
    .tag-row {
      display: flex;
      flex-wrap: wrap;
      gap: 6px;
    }
    .tag {
      display: inline-flex;
      align-items: center;
      min-height: 24px;
      padding: 0 8px;
      border-radius: 999px;
      background: var(--surface-soft);
      color: var(--muted);
      font-size: 12px;
      overflow-wrap: anywhere;
    }
    .notes {
      margin: 0;
      color: var(--muted);
      font-size: 13px;
      line-height: 1.5;
      overflow-wrap: anywhere;
    }
    .actions {
      display: grid;
      grid-template-columns: repeat(2, minmax(0, 1fr));
      gap: 8px;
    }
    .link {
      display: inline-flex;
      align-items: center;
      justify-content: center;
      min-height: 38px;
      border: 1px solid var(--line);
      border-radius: 8px;
      padding: 0 12px;
      background: var(--surface);
      color: var(--text);
      font-size: 14px;
    }
    .link.primary {
      border-color: var(--accent);
      background: var(--accent);
      color: #fff;
    }
    .link[aria-disabled="true"] {
      pointer-events: none;
      color: #9aa4b2;
      background: #f2f4f7;
    }
    .empty {
      display: none;
      margin: 28px 0 0;
      padding: 18px;
      border: 1px dashed var(--line);
      border-radius: 8px;
      color: var(--muted);
      background: var(--surface);
    }
    body.is-empty .empty { display: block; }
    .group.is-hidden, .card.is-hidden { display: none; }
    @media (max-width: 720px) {
      .shell { width: min(100vw - 24px, 1180px); padding-top: 18px; }
      .title-row { display: grid; align-items: start; }
      .summary { white-space: normal; }
      .controls { grid-template-columns: 1fr; }
      .cards { grid-template-columns: 1fr; }
      .card { min-height: 0; }
      .card-top { grid-template-columns: 40px 1fr; }
      .status { grid-column: 1 / -1; width: fit-content; }
      .actions { grid-template-columns: 1fr; }
    }
  </style>
</head>
<body>
  <main class="shell">
    <header>
      <div class="title-row">
        <div>
          <h1>{{.Title}}</h1>
          {{if .Subtitle}}<p class="subtitle">{{.Subtitle}}</p>{{end}}
        </div>
        <div class="summary"><span id="visible-count">0</span> 个入口</div>
      </div>
      <div class="controls">
        <input id="search" class="search" type="search" placeholder="搜索服务名、描述或标签" autocomplete="off">
        <div class="tags" aria-label="标签筛选">
          <button class="tag-filter is-active" type="button" data-tag="">全部</button>
          {{range .Tags}}<button class="tag-filter" type="button" data-tag="{{.}}">{{.}}</button>{{end}}
        </div>
      </div>
    </header>

    <section class="groups">
      {{range .Groups}}
      <section class="group" data-group-id="{{.ID}}">
        <div class="group-title">
          <h2>{{.Name}}</h2>
          <span class="group-count"><span class="group-visible-count">0</span> / {{len .Services}}</span>
        </div>
        <div class="cards">
          {{range .Services}}
          <article class="card" data-service-id="{{.ID}}" data-tags="{{range .Tags}} {{.}}{{end}}" data-search="{{.Name}} {{.Description}} {{range .Tags}}{{.}} {{end}}">
            <div class="card-top">
              <div class="icon" aria-hidden="true">{{if .Icon}}<img src="{{.Icon}}" alt="">{{else}}{{.DisplayIconText}}{{end}}</div>
              <div class="service-heading">
                <h3 class="service-name">{{.Name}}</h3>
                {{if .Description}}<p class="description">{{.Description}}</p>{{end}}
              </div>
              <span class="status" data-status="unknown" title="等待后台健康检查"><span class="dot"></span><span class="status-label">未知</span></span>
            </div>
            <div class="card-body">
              {{if .Tags}}<div class="tag-row">{{range .Tags}}<span class="tag">{{.}}</span>{{end}}</div>{{end}}
              {{if .Notes}}<p class="notes">{{.Notes}}</p>{{end}}
            </div>
            <div class="actions">
              {{if .InternalURL}}<a class="link primary" href="{{.InternalURL}}" target="_blank" rel="noreferrer">内网入口</a>{{else}}<span class="link" aria-disabled="true">无内网入口</span>{{end}}
              {{if .ExternalURL}}<a class="link" href="{{.ExternalURL}}" target="_blank" rel="noreferrer">外网入口</a>{{else}}<span class="link" aria-disabled="true">无外网入口</span>{{end}}
            </div>
          </article>
          {{end}}
        </div>
      </section>
      {{end}}
    </section>
    <p class="empty">没有匹配的服务入口。</p>
  </main>
  <script>
    const searchInput = document.querySelector("#search");
    const tagButtons = [...document.querySelectorAll(".tag-filter")];
    const cards = [...document.querySelectorAll(".card")];
    const groups = [...document.querySelectorAll(".group")];
    const visibleCount = document.querySelector("#visible-count");
    const statusLabels = { healthy: "正常", unhealthy: "异常", unknown: "未知", disabled: "未启用" };
    let activeTag = "";

    function normalizeText(value) {
      return (value || "").trim().toLowerCase();
    }

    function applyFilters() {
      const keyword = normalizeText(searchInput.value);
      let totalVisible = 0;

      for (const card of cards) {
        const text = normalizeText(card.dataset.search);
        const tags = normalizeText(card.dataset.tags).split(/\s+/).filter(Boolean);
        const matchedKeyword = !keyword || text.includes(keyword);
        const matchedTag = !activeTag || tags.includes(activeTag);
        const visible = matchedKeyword && matchedTag;
        card.classList.toggle("is-hidden", !visible);
        if (visible) totalVisible += 1;
      }

      for (const group of groups) {
        const visibleCards = [...group.querySelectorAll(".card:not(.is-hidden)")].length;
        group.classList.toggle("is-hidden", visibleCards === 0);
        group.querySelector(".group-visible-count").textContent = String(visibleCards);
      }

      visibleCount.textContent = String(totalVisible);
      document.body.classList.toggle("is-empty", totalVisible === 0);
    }

    async function refreshStatus() {
      try {
        const response = await fetch("/api/status", { cache: "no-store" });
        if (!response.ok) return;
        const payload = await response.json();
        for (const card of cards) {
          const status = payload.services?.[card.dataset.serviceId];
          if (!status) continue;
          const node = card.querySelector(".status");
          const value = status.status || "unknown";
          node.dataset.status = value;
          node.querySelector(".status-label").textContent = statusLabels[value] || "未知";
          node.title = status.error ? status.error : "最后检查状态：" + (statusLabels[value] || "未知");
        }
      } catch (_) {}
    }

    searchInput.addEventListener("input", applyFilters);
    for (const button of tagButtons) {
      button.addEventListener("click", () => {
        activeTag = normalizeText(button.dataset.tag);
        for (const item of tagButtons) item.classList.toggle("is-active", item === button);
        applyFilters();
      });
    }

    applyFilters();
    refreshStatus();
    setInterval(refreshStatus, 30000);
  </script>
</body>
</html>`
