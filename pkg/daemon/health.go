package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/maxesisn/nexus/pkg/config"
)

type restartState struct {
	consecutiveFailures int
	lastAttempt         time.Time
	exhausted           bool
}

var (
	httpProbeTimeout = 2 * time.Second
	tcpProbeTimeout  = 2 * time.Second
)

// HealthProbe checks process health beyond liveness.
type HealthProbe interface {
	Check(ctx context.Context) error
}

type execProbe struct {
	command string
	args    []string
}

type httpProbe struct {
	url string
}

type tcpProbe struct {
	addr string
}

// HealthMonitor periodically checks process liveness.
type HealthMonitor struct {
	logger       *slog.Logger
	manager      *ProcessManager
	interval     time.Duration
	maxRestarts  int
	baseBackoff  time.Duration
	restartState map[string]restartState
	probes       map[string]HealthProbe
}

// NewHealthMonitor creates a health monitor.
func NewHealthMonitor(
	logger *slog.Logger,
	manager *ProcessManager,
	interval time.Duration,
	maxRestarts int,
	services []config.ServiceSpec,
) (*HealthMonitor, error) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	if maxRestarts <= 0 {
		maxRestarts = 5
	}
	probes, err := buildProbeIndex(services)
	if err != nil {
		return nil, err
	}
	return &HealthMonitor{
		logger:       logger,
		manager:      manager,
		interval:     interval,
		maxRestarts:  maxRestarts,
		baseBackoff:  interval,
		restartState: make(map[string]restartState),
		probes:       probes,
	}, nil
}

// Run blocks and periodically logs process health status.
func (h *HealthMonitor) Run(ctx context.Context) {
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.checkOnce(ctx)
		}
	}
}

func (h *HealthMonitor) checkOnce(ctx context.Context) {
	states := h.manager.States()
	active := make(map[string]struct{}, len(states))
	now := time.Now()
	for _, st := range states {
		active[st.ID] = struct{}{}
		unhealthy := !st.Running
		if st.Running {
			if probe := h.probes[st.ID]; probe != nil {
				probeCtx, cancel := context.WithTimeout(ctx, h.interval)
				err := probe.Check(probeCtx)
				cancel()
				if err != nil {
					unhealthy = true
					h.logger.Warn("health probe failed", "id", st.ID, "service", st.Service, "err", err)
				}
			}
		}
		if unhealthy {
			h.logger.Warn("process unhealthy", "id", st.ID, "service", st.Service, "pid", st.PID)
			if h.shouldRestart(st.ID, now) {
				if err := h.manager.RestartProcess(ctx, st.ID); err != nil {
					h.logger.Warn("process restart failed", "id", st.ID, "service", st.Service, "err", err)
				} else {
					h.logger.Info("process restarted", "id", st.ID, "service", st.Service)
				}
			}
			continue
		}
		delete(h.restartState, st.ID)
		h.logger.Debug("process healthy", "id", st.ID, "service", st.Service, "pid", st.PID)
	}
	for id := range h.restartState {
		if _, ok := active[id]; !ok {
			delete(h.restartState, id)
		}
	}
}

func (h *HealthMonitor) shouldRestart(id string, now time.Time) bool {
	state := h.restartState[id]
	if state.exhausted {
		return false
	}
	if state.consecutiveFailures >= h.maxRestarts {
		state.exhausted = true
		h.restartState[id] = state
		h.logger.Error(fmt.Sprintf("max restart attempts reached for process %s", id), "id", id)
		return false
	}

	if state.consecutiveFailures > 0 {
		backoff := h.backoffForAttempt(state.consecutiveFailures)
		if now.Sub(state.lastAttempt) < backoff {
			return false
		}
	}

	state.consecutiveFailures++
	state.lastAttempt = now
	h.restartState[id] = state
	return true
}

func (h *HealthMonitor) backoffForAttempt(consecutiveFailures int) time.Duration {
	const maxBackoff = 5 * time.Minute
	backoff := h.baseBackoff
	for i := 1; i < consecutiveFailures; i++ {
		if backoff >= maxBackoff/2 {
			return maxBackoff
		}
		backoff *= 2
	}
	if backoff > maxBackoff {
		return maxBackoff
	}
	return backoff
}

func (p *execProbe) Check(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, p.command, p.args...)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("exec probe failed: %w", err)
	}
	return nil
}

func (p *httpProbe) Check(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.url, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: httpProbeTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("http probe status %d", resp.StatusCode)
	}
	return nil
}

func (p *tcpProbe) Check(context.Context) error {
	conn, err := net.DialTimeout("tcp", p.addr, tcpProbeTimeout)
	if err != nil {
		return err
	}
	return conn.Close()
}

func buildProbeIndex(services []config.ServiceSpec) (map[string]HealthProbe, error) {
	probes := make(map[string]HealthProbe)
	for _, svc := range services {
		if strings.TrimSpace(svc.HealthCheck) == "" {
			continue
		}
		probe, err := parseHealthProbe(svc.HealthCheck)
		if err != nil {
			return nil, fmt.Errorf("service %s has invalid health_check: %w", svc.Name, err)
		}
		instances := expandInstances(svc)
		for _, inst := range instances {
			probes[inst.ID] = probe
		}
	}
	return probes, nil
}

func parseHealthProbe(raw string) (HealthProbe, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("empty health_check")
	}
	if strings.HasPrefix(raw, "exec://") {
		execRaw := strings.TrimSpace(strings.TrimPrefix(raw, "exec://"))
		parts := strings.Fields(execRaw)
		if len(parts) == 0 {
			return nil, errors.New("exec health_check requires a command path")
		}
		return &execProbe{command: parts[0], args: append([]string(nil), parts[1:]...)}, nil
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	switch parsed.Scheme {
	case "http", "https":
		if parsed.Host == "" {
			return nil, errors.New("http health_check requires host")
		}
		return &httpProbe{url: parsed.String()}, nil
	case "tcp":
		if parsed.Host == "" {
			return nil, errors.New("tcp health_check requires host:port")
		}
		return &tcpProbe{addr: parsed.Host}, nil
	default:
		return nil, fmt.Errorf("unsupported health_check scheme %q", parsed.Scheme)
	}
}
