package server

import (
	"fmt"
	"net/http"
)

func New(configPath string) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>home-nav</title>
  <style>
    :root { color-scheme: light dark; font-family: system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; }
    body { margin: 0; min-height: 100vh; display: grid; place-items: center; background: #111827; color: #f9fafb; }
    main { width: min(720px, calc(100vw - 32px)); }
    h1 { margin: 0 0 12px; font-size: 32px; }
    p { margin: 8px 0; color: #cbd5e1; line-height: 1.6; }
    code { color: #93c5fd; }
  </style>
</head>
<body>
  <main>
    <h1>home-nav</h1>
    <p>项目骨架已创建。下一步会从 <code>%s</code> 读取服务配置，并渲染导航页。</p>
    <p>当前只提供 <code>/healthz</code> 占位健康检查。</p>
  </main>
</body>
</html>`, configPath)
	})

	return mux
}
