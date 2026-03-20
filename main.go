package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/camjac251/power-panel/internal/bmc"
	"github.com/camjac251/power-panel/internal/config"
	"github.com/camjac251/power-panel/internal/db"
	"github.com/camjac251/power-panel/internal/server"
)

var version = "dev"

//go:embed all:assets
var assetsFS embed.FS

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "/etc/power-panel/config.yaml", "path to config file")
	addr := flag.String("addr", "127.0.0.1:8080", "listen address (localhost only for Tailscale Serve)")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("loading config from %s: %w", *configPath, err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	store, err := db.Open(ctx, cfg.DataDir)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer store.Close()

	assets, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		return fmt.Errorf("loading embedded assets: %w", err)
	}

	bmcClient := bmc.NewClient(cfg.IPMI)
	defer bmcClient.Close()
	bmcClient.DiscoverCapabilities()
	wolClient := bmc.NewWoLClient(cfg.WoL)

	srv := server.New(cfg, store, bmcClient, wolClient, assets, version)

	slog.Info("starting power panel", "version", version, "addr", *addr, "server", cfg.Server.Name)
	return srv.ListenAndServe(ctx, *addr)
}
