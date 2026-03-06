package daemon

import (
	"context"
	"log/slog"
	"time"
)

// HealthMonitor periodically checks process liveness.
type HealthMonitor struct {
	logger   *slog.Logger
	manager  *ProcessManager
	interval time.Duration
	restart  map[string]time.Time
}

// NewHealthMonitor creates a health monitor.
func NewHealthMonitor(logger *slog.Logger, manager *ProcessManager, interval time.Duration) *HealthMonitor {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &HealthMonitor{
		logger:   logger,
		manager:  manager,
		interval: interval,
		restart:  make(map[string]time.Time),
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
		delete(h.restart, st.ID)
		h.logger.Debug("process healthy", "id", st.ID, "service", st.Service, "pid", st.PID)
	}
	for id := range h.restart {
		if _, ok := active[id]; !ok {
			delete(h.restart, id)
		}
	}
}

func (h *HealthMonitor) shouldRestart(id string, now time.Time) bool {
	if last, ok := h.restart[id]; ok && now.Sub(last) < h.interval {
		return false
	}
	h.restart[id] = now
	return true
}
