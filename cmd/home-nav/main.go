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

	if _, err := os.Stat(*configPath); err != nil {
		slog.Warn("config file is not readable yet; starting placeholder server", "path", *configPath, "error", err)
	}

	srv := server.New(*configPath)
	slog.Info("home-nav listening", "addr", *addr, "config", *configPath)
	if err := http.ListenAndServe(*addr, srv); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
