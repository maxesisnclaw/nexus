package daemon

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"nexus/pkg/config"
)

func TestProcessManagerStartStopService(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	pm := NewProcessManager(logger, 2*time.Second)
	spec := config.ServiceSpec{
		Name:    "sleepy",
		Type:    "singleton",
		Runtime: "binary",
		Binary:  "/bin/sleep",
		Args:    []string{"30"},
	}

	if err := pm.StartService(context.Background(), spec); err != nil {
		t.Fatalf("StartService() error = %v", err)
	}
	if !pm.IsRunning("sleepy") {
		t.Fatal("expected process to be running")
	}
	if err := pm.StopService("sleepy"); err != nil {
		t.Fatalf("StopService() error = %v", err)
	}
	if pm.IsRunning("sleepy") {
		t.Fatal("expected process to be stopped")
	}
}

func TestDaemonStartStop(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cfg := &config.Config{
		Daemon: config.DaemonConfig{
			HealthInterval: config.Duration{Duration: 100 * time.Millisecond},
			ShutdownGrace:  config.Duration{Duration: 2 * time.Second},
		},
		Services: []config.ServiceSpec{{
			Name:    "sleepy",
			Type:    "singleton",
			Runtime: "binary",
			Binary:  "/bin/sleep",
			Args:    []string{"30"},
		}},
	}

	d := New(cfg, logger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := d.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	time.Sleep(120 * time.Millisecond)
	states := d.ProcessStates()
	if len(states) != 1 || !states[0].Running {
		t.Fatalf("unexpected states: %+v", states)
	}

	if err := d.Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
}
