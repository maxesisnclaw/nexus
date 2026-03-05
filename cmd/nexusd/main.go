package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"nexus/pkg/config"
	"nexus/pkg/daemon"
)

func main() {
	var configPath string
	flag.StringVar(&configPath, "config", "./nexus.toml", "path to nexus TOML config")
	flag.Parse()

	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config failed: %v\n", err)
		os.Exit(1)
	}

	logger := newLogger(cfg.Daemon.LogLevel)
	d := daemon.New(cfg, logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := d.Start(ctx); err != nil {
		logger.Error("daemon start failed", "err", err)
		os.Exit(1)
	}
	logger.Info("nexus daemon started")

	<-ctx.Done()
	if err := d.Stop(); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("daemon stop failed", "err", err)
		os.Exit(1)
	}
	logger.Info("nexus daemon stopped")
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l})
	return slog.New(h)
}
