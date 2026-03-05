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
}

// NewHealthMonitor creates a health monitor.
func NewHealthMonitor(logger *slog.Logger, manager *ProcessManager, interval time.Duration) *HealthMonitor {
	return &HealthMonitor{logger: logger, manager: manager, interval: interval}
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
			h.checkOnce()
		}
	}
}

func (h *HealthMonitor) checkOnce() {
	states := h.manager.States()
	for _, st := range states {
		if !st.Running {
			h.logger.Warn("process unhealthy", "id", st.ID, "service", st.Service, "pid", st.PID)
			continue
		}
		h.logger.Debug("process healthy", "id", st.ID, "service", st.Service, "pid", st.PID)
	}
}
