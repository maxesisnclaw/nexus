package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

type restartState struct {
	consecutiveFailures int
	lastAttempt         time.Time
	exhausted           bool
}

// HealthMonitor periodically checks process liveness.
type HealthMonitor struct {
	logger       *slog.Logger
	manager      *ProcessManager
	interval     time.Duration
	maxRestarts  int
	baseBackoff  time.Duration
	restartState map[string]restartState
}

// NewHealthMonitor creates a health monitor.
func NewHealthMonitor(logger *slog.Logger, manager *ProcessManager, interval time.Duration, maxRestarts int) *HealthMonitor {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	if maxRestarts <= 0 {
		maxRestarts = 5
	}
	return &HealthMonitor{
		logger:       logger,
		manager:      manager,
		interval:     interval,
		maxRestarts:  maxRestarts,
		baseBackoff:  interval,
		restartState: make(map[string]restartState),
	}
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
		if !st.Running {
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
