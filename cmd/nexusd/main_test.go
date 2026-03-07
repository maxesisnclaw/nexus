package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"

	"github.com/maxesisn/nexus/pkg/config"
)

func TestNewLoggerLevelMapping(t *testing.T) {
	tests := []struct {
		name    string
		level   string
		debugOn bool
		errorOn bool
	}{
		{name: "debug", level: "debug", debugOn: true, errorOn: true},
		{name: "warn", level: "warn", debugOn: false, errorOn: true},
		{name: "error", level: "error", debugOn: false, errorOn: true},
		{name: "default", level: "unknown", debugOn: false, errorOn: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logger := newLogger(tc.level)
			ctx := context.Background()
			if got := logger.Enabled(ctx, slog.LevelDebug); got != tc.debugOn {
				t.Fatalf("debug enabled mismatch: got=%v want=%v", got, tc.debugOn)
			}
			if got := logger.Enabled(ctx, slog.LevelError); got != tc.errorOn {
				t.Fatalf("error enabled mismatch: got=%v want=%v", got, tc.errorOn)
			}
		})
	}
}

type stubDaemonRunner struct {
	startErr error
	stopErr  error
	onStart  func(context.Context)

	startCalls int
	stopCalls  int
}

func (s *stubDaemonRunner) Start(ctx context.Context) error {
	s.startCalls++
	if s.onStart != nil {
		s.onStart(ctx)
	}
	return s.startErr
}

func (s *stubDaemonRunner) Stop() error {
	s.stopCalls++
	return s.stopErr
}

func TestRunParseError(t *testing.T) {
	code := run([]string{"-invalid"})
	if code != 2 {
		t.Fatalf("run() code = %d, want 2", code)
	}
}

func TestRunLoadConfigFailure(t *testing.T) {
	origLoad := loadConfig
	origNewDaemon := newDaemon
	origNotify := notifyContext
	t.Cleanup(func() {
		loadConfig = origLoad
		newDaemon = origNewDaemon
		notifyContext = origNotify
	})

	loadConfig = func(string) (*config.Config, error) {
		return nil, errors.New("boom")
	}

	code := run(nil)
	if code != 1 {
		t.Fatalf("run() code = %d, want 1", code)
	}
}

func TestRunStartFailure(t *testing.T) {
	origLoad := loadConfig
	origNewDaemon := newDaemon
	origNotify := notifyContext
	t.Cleanup(func() {
		loadConfig = origLoad
		newDaemon = origNewDaemon
		notifyContext = origNotify
	})

	cfg := &config.Config{Daemon: config.DaemonConfig{LogLevel: "info"}}
	stub := &stubDaemonRunner{startErr: errors.New("start failed")}

	loadConfig = func(string) (*config.Config, error) {
		return cfg, nil
	}
	newDaemon = func(*config.Config, *slog.Logger) daemonRunner {
		return stub
	}
	notifyContext = func(context.Context, ...os.Signal) (context.Context, context.CancelFunc) {
		return context.WithCancel(context.Background())
	}

	code := run(nil)
	if code != 1 {
		t.Fatalf("run() code = %d, want 1", code)
	}
	if stub.startCalls != 1 {
		t.Fatalf("Start() calls = %d, want 1", stub.startCalls)
	}
	if stub.stopCalls != 0 {
		t.Fatalf("Stop() calls = %d, want 0", stub.stopCalls)
	}
}

func TestRunStartAndStopLifecycle(t *testing.T) {
	origLoad := loadConfig
	origNewDaemon := newDaemon
	origNotify := notifyContext
	t.Cleanup(func() {
		loadConfig = origLoad
		newDaemon = origNewDaemon
		notifyContext = origNotify
	})

	cfg := &config.Config{Daemon: config.DaemonConfig{LogLevel: "info"}}
	var cancel context.CancelFunc
	stub := &stubDaemonRunner{
		onStart: func(context.Context) {
			cancel()
		},
	}

	loadConfig = func(string) (*config.Config, error) {
		return cfg, nil
	}
	newDaemon = func(*config.Config, *slog.Logger) daemonRunner {
		return stub
	}
	notifyContext = func(parent context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
		ctx, c := context.WithCancel(parent)
		cancel = c
		return ctx, c
	}

	code := run(nil)
	if code != 0 {
		t.Fatalf("run() code = %d, want 0", code)
	}
	if stub.startCalls != 1 {
		t.Fatalf("Start() calls = %d, want 1", stub.startCalls)
	}
	if stub.stopCalls != 1 {
		t.Fatalf("Stop() calls = %d, want 1", stub.stopCalls)
	}
}

func TestRunStopCanceledAllowed(t *testing.T) {
	origLoad := loadConfig
	origNewDaemon := newDaemon
	origNotify := notifyContext
	t.Cleanup(func() {
		loadConfig = origLoad
		newDaemon = origNewDaemon
		notifyContext = origNotify
	})

	cfg := &config.Config{Daemon: config.DaemonConfig{LogLevel: "info"}}
	var cancel context.CancelFunc
	stub := &stubDaemonRunner{
		stopErr: context.Canceled,
		onStart: func(context.Context) {
			cancel()
		},
	}

	loadConfig = func(string) (*config.Config, error) {
		return cfg, nil
	}
	newDaemon = func(*config.Config, *slog.Logger) daemonRunner {
		return stub
	}
	notifyContext = func(parent context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
		ctx, c := context.WithCancel(parent)
		cancel = c
		return ctx, c
	}

	code := run(nil)
	if code != 0 {
		t.Fatalf("run() code = %d, want 0", code)
	}
}

func TestRunStopFailure(t *testing.T) {
	origLoad := loadConfig
	origNewDaemon := newDaemon
	origNotify := notifyContext
	t.Cleanup(func() {
		loadConfig = origLoad
		newDaemon = origNewDaemon
		notifyContext = origNotify
	})

	cfg := &config.Config{Daemon: config.DaemonConfig{LogLevel: "info"}}
	var cancel context.CancelFunc
	stub := &stubDaemonRunner{
		stopErr: errors.New("stop failed"),
		onStart: func(context.Context) {
			cancel()
		},
	}

	loadConfig = func(string) (*config.Config, error) {
		return cfg, nil
	}
	newDaemon = func(*config.Config, *slog.Logger) daemonRunner {
		return stub
	}
	notifyContext = func(parent context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
		ctx, c := context.WithCancel(parent)
		cancel = c
		return ctx, c
	}

	code := run(nil)
	if code != 1 {
		t.Fatalf("run() code = %d, want 1", code)
	}
	if stub.stopCalls != 1 {
		t.Fatalf("Stop() calls = %d, want 1", stub.stopCalls)
	}
}
