package daemon

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/maxesisn/nexus/pkg/config"
	"github.com/maxesisn/nexus/pkg/registry"
	"github.com/maxesisn/nexus/pkg/transport"
)

// Daemon orchestrates managed services.
type Daemon struct {
	cfg    *config.Config
	logger *slog.Logger

	pm       *ProcessManager
	health   *HealthMonitor
	registry *registry.Registry
	control  *ControlServer
	tcpNoise transport.Listener

	mu      sync.Mutex
	started bool
	stopped bool
	cancel  context.CancelFunc
	done    chan struct{}
	wg      sync.WaitGroup
}

var (
	// ErrDaemonAlreadyStarted indicates Start was called while daemon is already running.
	ErrDaemonAlreadyStarted = errors.New("daemon already started")
	// ErrDaemonRestartAfterStop indicates the daemon instance was already stopped and cannot restart.
	ErrDaemonRestartAfterStop = errors.New("daemon cannot be restarted after Stop")
)

// New creates a daemon instance.
func New(cfg *config.Config, logger *slog.Logger) (*Daemon, error) {
	if cfg == nil {
		return nil, errors.New("daemon: config must not be nil")
	}
	if logger == nil {
		logger = slog.Default()
	}
	nodeID := "local"
	if host, err := os.Hostname(); err == nil && host != "" {
		nodeID = host
	}
	reg := registry.New(nodeID)
	pm := NewProcessManager(logger, cfg.Daemon.ShutdownGrace.Duration)
	health, err := NewHealthMonitor(logger, pm, cfg.Daemon.HealthInterval.Duration, 5, cfg.Services)
	if err != nil {
		reg.Close()
		return nil, err
	}
	return &Daemon{cfg: cfg, logger: logger, pm: pm, health: health, registry: reg}, nil
}

// Start launches services and starts the health loop.
func (d *Daemon) Start(ctx context.Context) error {
	d.mu.Lock()
	if d.started {
		d.mu.Unlock()
		return ErrDaemonAlreadyStarted
	}
	if d.stopped {
		d.mu.Unlock()
		return ErrDaemonRestartAfterStop
	}
	runCtx, cancel := context.WithCancel(ctx)
	d.cancel = cancel
	d.done = make(chan struct{})
	d.started = true
	d.wg.Add(1)
	d.mu.Unlock()

	if d.cfg.Daemon.Listen != "" {
		if _, _, err := net.SplitHostPort(d.cfg.Daemon.Listen); err != nil {
			d.resetStartState(cancel)
			return fmt.Errorf("invalid daemon.listen address %q: %w", d.cfg.Daemon.Listen, err)
		}
		if d.cfg.Daemon.NoiseKeyFile == "" {
			d.logger.Warn("refusing daemon tcp listen without noise key file", "listen", d.cfg.Daemon.Listen)
			d.resetStartState(cancel)
			return errors.New("daemon.listen requires daemon.noise_key_file for encrypted tcp transport")
		}
		priv, pub, err := transport.LoadOrGenerateKey(d.cfg.Daemon.NoiseKeyFile)
		if err != nil {
			d.resetStartState(cancel)
			return fmt.Errorf("load noise key: %w", err)
		}
		tcp := transport.NewNoiseTCPTransport(priv, pub, d.cfg.Daemon.TrustedKeys)
		ln, err := tcp.Listen(runCtx, d.cfg.Daemon.Listen)
		if err != nil {
			d.resetStartState(cancel)
			return fmt.Errorf("start noise tcp listener: %w", err)
		}
		d.tcpNoise = ln
		d.logger.Info("Noise TCP listener started", "listen", ln.Addr(), "public_key", hex.EncodeToString(pub))
	}

	if d.cfg.Daemon.Socket != "" {
		d.control = NewControlServer(d, d.registry, d.logger, time.Now())
		if err := d.control.Start(d.cfg.Daemon.Socket); err != nil {
			_ = d.shutdownNoiseTCP()
			d.resetStartState(cancel)
			return err
		}
	}

	ordered, err := ResolveStartOrder(d.cfg.Services)
	if err != nil {
		_ = d.shutdownControl()
		_ = d.shutdownNoiseTCP()
		d.resetStartState(cancel)
		return err
	}
	for _, svc := range ordered {
		if err := d.pm.StartService(runCtx, svc); err != nil {
			stopErr := d.pm.StopAll()
			controlErr := d.shutdownControl()
			noiseErr := d.shutdownNoiseTCP()
			cancel()
			d.resetStartState(nil)
			return fmt.Errorf("start service %s: %w", svc.Name, errors.Join(err, stopErr, controlErr, noiseErr))
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
	d.stopped = true
	d.cancel = nil
	d.done = nil
	d.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	d.wg.Wait()
	noiseErr := d.shutdownNoiseTCP()
	controlErr := d.shutdownControl()
	stopErr := d.pm.StopAll()
	var regErr error
	if d.registry != nil {
		d.registry.Close()
	}
	return errors.Join(noiseErr, controlErr, stopErr, regErr)
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

func (d *Daemon) resetStartState(cancel context.CancelFunc) {
	if cancel != nil {
		cancel()
	}
	d.mu.Lock()
	d.started = false
	d.cancel = nil
	d.done = nil
	d.mu.Unlock()
	d.wg.Done() // Balance Start() Add when health goroutine is not launched.
}

func (d *Daemon) shutdownControl() error {
	d.mu.Lock()
	control := d.control
	d.control = nil
	d.mu.Unlock()
	if control != nil {
		return control.Close()
	}
	return nil
}

func (d *Daemon) shutdownNoiseTCP() error {
	d.mu.Lock()
	ln := d.tcpNoise
	d.tcpNoise = nil
	d.mu.Unlock()
	if ln != nil {
		return ln.Close()
	}
	return nil
}

// ResolveStartOrder returns services in dependency-satisfying order.
// Uses Kahn's algorithm for topological sort.
func ResolveStartOrder(services []config.ServiceSpec) ([]config.ServiceSpec, error) {
	if len(services) == 0 {
		return nil, nil
	}
	byName := make(map[string]config.ServiceSpec, len(services))
	indexByName := make(map[string]int, len(services))
	for i, svc := range services {
		if _, exists := byName[svc.Name]; exists {
			return nil, fmt.Errorf("duplicate service name: %s", svc.Name)
		}
		byName[svc.Name] = svc
		indexByName[svc.Name] = i
	}

	indegree := make(map[string]int, len(services))
	dependents := make(map[string][]string, len(services))
	depsByName := make(map[string][]string, len(services))
	for _, svc := range services {
		uniqueDeps := make(map[string]struct{}, len(svc.DependsOn))
		for _, dep := range svc.DependsOn {
			if dep == svc.Name {
				return nil, fmt.Errorf("circular dependency: %s -> %s", svc.Name, svc.Name)
			}
			if _, ok := byName[dep]; !ok {
				return nil, fmt.Errorf("service %s depends on unknown service %s", svc.Name, dep)
			}
			if _, seen := uniqueDeps[dep]; seen {
				continue
			}
			uniqueDeps[dep] = struct{}{}
			depsByName[svc.Name] = append(depsByName[svc.Name], dep)
			dependents[dep] = append(dependents[dep], svc.Name)
			indegree[svc.Name]++
		}
		if _, ok := indegree[svc.Name]; !ok {
			indegree[svc.Name] = 0
		}
	}

	queue := make([]string, 0, len(services))
	for _, svc := range services {
		if indegree[svc.Name] == 0 {
			queue = append(queue, svc.Name)
		}
	}

	out := make([]config.ServiceSpec, 0, len(services))
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		out = append(out, byName[name])

		next := dependents[name]
		if len(next) > 1 {
			// Keep deterministic order while preserving original config order.
			sortByConfigIndex(next, indexByName)
		}
		for _, dependent := range next {
			indegree[dependent]--
			if indegree[dependent] == 0 {
				queue = append(queue, dependent)
			}
		}
	}
	if len(out) == len(services) {
		return out, nil
	}

	remaining := make(map[string]struct{})
	for name, deg := range indegree {
		if deg > 0 {
			remaining[name] = struct{}{}
		}
	}
	cycle := findDependencyCycle(services, depsByName, remaining)
	if len(cycle) == 0 {
		return nil, errors.New("circular dependency detected")
	}
	return nil, fmt.Errorf("circular dependency: %s", strings.Join(cycle, " -> "))
}

func sortByConfigIndex(names []string, indexByName map[string]int) {
	for i := 0; i < len(names)-1; i++ {
		for j := i + 1; j < len(names); j++ {
			if indexByName[names[j]] < indexByName[names[i]] {
				names[i], names[j] = names[j], names[i]
			}
		}
	}
}

func findDependencyCycle(services []config.ServiceSpec, depsByName map[string][]string, remaining map[string]struct{}) []string {
	const (
		unvisited = iota
		visiting
		visited
	)
	state := make(map[string]int, len(remaining))
	stack := make([]string, 0, len(remaining))
	stackPos := make(map[string]int, len(remaining))

	var dfs func(string) []string
	dfs = func(name string) []string {
		state[name] = visiting
		stackPos[name] = len(stack)
		stack = append(stack, name)
		for _, dep := range depsByName[name] {
			if _, ok := remaining[dep]; !ok {
				continue
			}
			switch state[dep] {
			case unvisited:
				if cycle := dfs(dep); len(cycle) > 0 {
					return cycle
				}
			case visiting:
				idx := stackPos[dep]
				cycle := append([]string(nil), stack[idx:]...)
				cycle = append(cycle, dep)
				return cycle
			}
		}
		stack = stack[:len(stack)-1]
		delete(stackPos, name)
		state[name] = visited
		return nil
	}

	for _, svc := range services {
		if _, ok := remaining[svc.Name]; !ok {
			continue
		}
		if state[svc.Name] != unvisited {
			continue
		}
		if cycle := dfs(svc.Name); len(cycle) > 0 {
			return cycle
		}
	}
	return nil
}
