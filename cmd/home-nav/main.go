package main

import (
	"flag"
	"log/slog"
	"net/http"
	"os"

	"github.com/saurick/home-nav/internal/server"
)

func main() {
	addr := flag.String("addr", ":8080", "HTTP listen address")
	configPath := flag.String("config", "services.yaml", "services config path")
	flag.Parse()

	srv, err := server.New(*configPath)
	if err != nil {
		slog.Error("配置加载失败", "path", *configPath, "error", err)
		os.Exit(1)
	}

	slog.Info("home-nav listening", "addr", *addr, "config", *configPath)
	if err := http.ListenAndServe(*addr, srv); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
