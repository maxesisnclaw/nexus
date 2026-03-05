package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"nexus/pkg/config"
)

// Daemon orchestrates managed services.
type Daemon struct {
	cfg    *config.Config
	logger *slog.Logger

	pm     *ProcessManager
	health *HealthMonitor

	mu      sync.Mutex
	started bool
	cancel  context.CancelFunc
}

// New creates a daemon instance.
func New(cfg *config.Config, logger *slog.Logger) *Daemon {
	pm := NewProcessManager(logger, cfg.Daemon.ShutdownGrace.Duration)
	health := NewHealthMonitor(logger, pm, cfg.Daemon.HealthInterval.Duration)
	return &Daemon{cfg: cfg, logger: logger, pm: pm, health: health}
}

// Start launches services and starts the health loop.
func (d *Daemon) Start(ctx context.Context) error {
	d.mu.Lock()
	if d.started {
		d.mu.Unlock()
		return errors.New("daemon already started")
	}
	runCtx, cancel := context.WithCancel(ctx)
	d.cancel = cancel
	d.started = true
	d.mu.Unlock()

	// We intentionally use config order for startup to keep behavior explicit
	// and simple; dependency-aware scheduling can be added later if needed.
	for _, svc := range d.cfg.Services {
		if err := d.pm.StartService(runCtx, svc); err != nil {
			_ = d.pm.StopAll()
			cancel()
			return fmt.Errorf("start service %s: %w", svc.Name, err)
		}
	}

	go d.health.Run(runCtx)
	return nil
}

// Stop stops health loop and all services.
func (d *Daemon) Stop() error {
	d.mu.Lock()
	if !d.started {
		d.mu.Unlock()
		return nil
	}
	cancel := d.cancel
	d.started = false
	d.cancel = nil
	d.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	return d.pm.StopAll()
}

// RestartService restarts one configured service by name.
func (d *Daemon) RestartService(ctx context.Context, name string) error {
	for _, svc := range d.cfg.Services {
		if svc.Name == name {
			return d.pm.RestartService(ctx, svc)
		}
	}
	return fmt.Errorf("service %s not found", name)
}

// ProcessStates returns current process snapshot.
func (d *Daemon) ProcessStates() []ProcessState {
	return d.pm.States()
}
