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

	"github.com/maxesisn/nexus/pkg/config"
	"github.com/maxesisn/nexus/pkg/daemon"
)

type daemonRunner interface {
	Start(context.Context) error
	Stop() error
}

var (
	loadConfig    = config.Load
	newDaemon     = func(cfg *config.Config, logger *slog.Logger) (daemonRunner, error) { return daemon.New(cfg, logger) }
	notifyContext = signal.NotifyContext
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	flags := flag.NewFlagSet("nexusd", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)

	var configPath string
	flags.StringVar(&configPath, "config", "./nexus.toml", "path to nexus TOML config")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config failed: %v\n", err)
		return 1
	}

	logger := newLogger(cfg.Daemon.LogLevel)
	d, err := newDaemon(cfg, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create daemon failed: %v\n", err)
		return 1
	}

	ctx, stop := notifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := d.Start(ctx); err != nil {
		logger.Error("daemon start failed", "err", err)
		return 1
	}
	logger.Info("nexus daemon started")

	<-ctx.Done()
	if err := d.Stop(); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("daemon stop failed", "err", err)
		return 1
	}
	logger.Info("nexus daemon stopped")
	return 0
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
