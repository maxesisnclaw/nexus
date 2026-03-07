package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/maxesisn/nexus/pkg/config"
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
	done    chan struct{}
	wg      sync.WaitGroup
}

// New creates a daemon instance.
func New(cfg *config.Config, logger *slog.Logger) (*Daemon, error) {
	if cfg == nil {
		return nil, errors.New("daemon: config must not be nil")
	}
	if logger == nil {
		logger = slog.Default()
	}
	pm := NewProcessManager(logger, cfg.Daemon.ShutdownGrace.Duration)
	health := NewHealthMonitor(logger, pm, cfg.Daemon.HealthInterval.Duration, 5)
	return &Daemon{cfg: cfg, logger: logger, pm: pm, health: health}, nil
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
	d.done = make(chan struct{})
	d.started = true
	d.wg.Add(1)
	d.mu.Unlock()

	// We intentionally use config order for startup to keep behavior explicit
	// and simple; dependency-aware scheduling can be added later if needed.
	for _, svc := range d.cfg.Services {
		if err := d.pm.StartService(runCtx, svc); err != nil {
			stopErr := d.pm.StopAll()
			cancel()
			d.mu.Lock()
			d.started = false
			d.cancel = nil
			d.done = nil
			d.mu.Unlock()
			d.wg.Done() // Balance Start() Add when health goroutine is not launched.
			return fmt.Errorf("start service %s: %w", svc.Name, errors.Join(err, stopErr))
		}
	}

	go func(done chan struct{}) {
		defer d.wg.Done()
		defer close(done)
		d.health.Run(runCtx)
	}(d.done)
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
	d.done = nil
	d.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	d.wg.Wait()
	stopErr := d.pm.StopAll()
	return stopErr
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
