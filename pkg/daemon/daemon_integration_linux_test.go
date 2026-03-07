//go:build integration && linux

package daemon

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/maxesisn/nexus/pkg/config"
)

func TestIntegrationDaemonLifecycle(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cfg := &config.Config{
		Daemon: config.DaemonConfig{
			HealthInterval: config.Duration{Duration: 100 * time.Millisecond},
			ShutdownGrace:  config.Duration{Duration: 2 * time.Second},
		},
		Services: []config.ServiceSpec{{
			Name:    "worker",
			Type:    "singleton",
			Runtime: "binary",
			Binary:  "/bin/sh",
			Args: []string{
				"-c",
				`trap "exit 0" TERM; while true; do sleep 1; done`,
			},
		}},
	}

	d := New(cfg, logger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := d.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		states := d.ProcessStates()
		if len(states) == 1 && states[0].Running {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	states := d.ProcessStates()
	if len(states) != 1 || !states[0].Running {
		t.Fatalf("expected running process, got %+v", states)
	}

	if err := d.Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if got := len(d.ProcessStates()); got != 0 {
		t.Fatalf("expected empty process state after stop, got %d", got)
	}
}
