package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"nexus/pkg/config"
)

// ProcessState describes a managed process state snapshot.
type ProcessState struct {
	ID      string
	Service string
	PID     int
	Running bool
}

// ManagedProcess keeps runtime state for one process instance.
type ManagedProcess struct {
	ID      string
	Service string
	Spec    config.ServiceSpec
	Args    []string

	cmd           *exec.Cmd
	containerName string
	exited        chan struct{}
}

// ProcessManager starts/stops and tracks service processes.
type ProcessManager struct {
	mu       sync.RWMutex
	procs    map[string]*ManagedProcess
	logger   *slog.Logger
	stopWait time.Duration
	docker   dockerRuntime
}

// NewProcessManager creates a process manager.
func NewProcessManager(logger *slog.Logger, stopWait time.Duration) *ProcessManager {
	return &ProcessManager{
		procs:    make(map[string]*ManagedProcess),
		logger:   logger,
		stopWait: stopWait,
		docker:   newDockerRuntime(logger),
	}
}

// StartService starts one service and all configured instances.
func (m *ProcessManager) StartService(ctx context.Context, spec config.ServiceSpec) error {
	instances := expandInstances(spec)
	startedIDs := make([]string, 0, len(instances))
	for _, inst := range instances {
		if err := m.startProcess(ctx, inst); err != nil {
			for i := len(startedIDs) - 1; i >= 0; i-- {
				_ = m.StopProcess(startedIDs[i])
			}
			return err
		}
		startedIDs = append(startedIDs, inst.ID)
	}
	return nil
}

// StopService stops all processes that belong to a service.
func (m *ProcessManager) StopService(serviceName string) error {
	m.mu.RLock()
	ids := make([]string, 0)
	for id, p := range m.procs {
		if p.Service == serviceName {
			ids = append(ids, id)
		}
	}
	m.mu.RUnlock()
	var joined error
	for _, id := range ids {
		if err := m.StopProcess(id); err != nil {
			joined = errors.Join(joined, err)
		}
	}
	return joined
}

// RestartService restarts every process under a service name.
func (m *ProcessManager) RestartService(ctx context.Context, spec config.ServiceSpec) error {
	if err := m.StopService(spec.Name); err != nil {
		return err
	}
	return m.StartService(ctx, spec)
}

// RestartProcess restarts one managed process by id.
func (m *ProcessManager) RestartProcess(ctx context.Context, id string) error {
	m.mu.RLock()
	proc, ok := m.procs[id]
	if !ok {
		m.mu.RUnlock()
		return fmt.Errorf("process %s not found", id)
	}
	next := &ManagedProcess{
		ID:      proc.ID,
		Service: proc.Service,
		Spec:    proc.Spec,
		Args:    append([]string(nil), proc.Args...),
	}
	m.mu.RUnlock()

	if ctx == nil {
		ctx = context.Background()
	}
	if err := m.StopProcess(id); err != nil {
		return err
	}
	return m.startProcess(ctx, next)
}

// StopAll stops all managed processes.
func (m *ProcessManager) StopAll() error {
	m.mu.RLock()
	ids := make([]string, 0, len(m.procs))
	for id := range m.procs {
		ids = append(ids, id)
	}
	m.mu.RUnlock()
	var joined error
	for _, id := range ids {
		if err := m.StopProcess(id); err != nil {
			joined = errors.Join(joined, err)
		}
	}
	return joined
}

// StopProcess gracefully terminates a process and escalates to SIGKILL on timeout.
func (m *ProcessManager) StopProcess(id string) error {
	m.mu.RLock()
	proc, ok := m.procs[id]
	m.mu.RUnlock()
	if !ok {
		return nil
	}
	if proc.Spec.Runtime == "docker" {
		if proc.containerName != "" {
			if err := m.docker.Stop(proc.containerName, m.stopWait); err != nil {
				return fmt.Errorf("stop docker %s: %w", id, err)
			}
		}
		m.deleteProcess(id)
		return nil
	}
	if proc.cmd == nil || proc.cmd.Process == nil {
		m.deleteProcess(id)
		return nil
	}

	if err := proc.cmd.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("signal SIGTERM %s: %w", id, err)
	}
	waitCh := proc.exited
	if waitCh == nil {
		m.deleteProcessIfMatch(id, proc)
		return nil
	}
	timer := time.NewTimer(m.stopWait)
	defer timer.Stop()
	select {
	case <-waitCh:
		return nil
	case <-timer.C:
	}

	if err := proc.cmd.Process.Signal(syscall.SIGKILL); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("signal SIGKILL %s: %w", id, err)
	}
	select {
	case <-waitCh:
	case <-time.After(500 * time.Millisecond):
	}
	m.deleteProcessIfMatch(id, proc)
	return nil
}

// States returns a snapshot of all process states.
func (m *ProcessManager) States() []ProcessState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	states := make([]ProcessState, 0, len(m.procs))
	for id, p := range m.procs {
		pid := 0
		running := false
		if p.Spec.Runtime == "docker" {
			alive, err := m.docker.IsRunning(p.containerName)
			running = err == nil && alive
		} else if p.cmd != nil && p.cmd.Process != nil {
			pid = p.cmd.Process.Pid
			alive, err := isPIDAlive(pid)
			running = err == nil && alive
		}
		states = append(states, ProcessState{ID: id, Service: p.Service, PID: pid, Running: running})
	}
	return states
}

// IsRunning reports whether process id is currently alive.
func (m *ProcessManager) IsRunning(id string) bool {
	m.mu.RLock()
	proc, ok := m.procs[id]
	m.mu.RUnlock()
	if !ok {
		return false
	}
	if proc.Spec.Runtime == "docker" {
		alive, err := m.docker.IsRunning(proc.containerName)
		return err == nil && alive
	}
	if proc.cmd == nil || proc.cmd.Process == nil {
		return false
	}
	alive, err := isPIDAlive(proc.cmd.Process.Pid)
	return err == nil && alive
}

func (m *ProcessManager) startProcess(ctx context.Context, proc *ManagedProcess) error {
	m.mu.RLock()
	_, exists := m.procs[proc.ID]
	m.mu.RUnlock()
	if exists {
		return fmt.Errorf("process %s already exists", proc.ID)
	}

	if proc.Spec.Runtime == "docker" {
		containerName, err := m.docker.Start(ctx, proc)
		if err != nil {
			return fmt.Errorf("start docker %s: %w", proc.ID, err)
		}
		proc.containerName = containerName
		m.mu.Lock()
		m.procs[proc.ID] = proc
		m.mu.Unlock()
		m.logger.Info("service process started", "id", proc.ID, "service", proc.Service, "runtime", "docker", "container", containerName)
		return nil
	}
	if proc.Spec.Runtime != "binary" {
		return fmt.Errorf("service %s runtime %s not supported in process manager", proc.Service, proc.Spec.Runtime)
	}

	cmd := exec.CommandContext(ctx, proc.Spec.Binary, proc.Args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start process %s: %w", proc.ID, err)
	}
	proc.cmd = cmd
	proc.exited = make(chan struct{})

	m.mu.Lock()
	m.procs[proc.ID] = proc
	m.mu.Unlock()

	m.logger.Info("service process started", "id", proc.ID, "service", proc.Service, "pid", cmd.Process.Pid)
	go func(p *ManagedProcess, c *exec.Cmd, exited chan struct{}) {
		err := c.Wait()
		close(exited)
		if err != nil {
			m.logger.Warn("service process exited with error", "id", p.ID, "err", err)
		} else {
			m.logger.Info("service process exited", "id", p.ID)
		}
		m.deleteProcessIfMatch(p.ID, p)
	}(proc, cmd, proc.exited)
	return nil
}

func (m *ProcessManager) deleteProcess(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.procs, id)
}

func (m *ProcessManager) deleteProcessIfMatch(id string, expected *ManagedProcess) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.procs[id]
	if !ok || current != expected {
		return
	}
	delete(m.procs, id)
}

func expandInstances(spec config.ServiceSpec) []*ManagedProcess {
	if spec.Type == "worker" && len(spec.Instances) > 0 {
		out := make([]*ManagedProcess, 0, len(spec.Instances))
		for i, inst := range spec.Instances {
			args := spec.Args
			if len(inst.Args) > 0 {
				args = inst.Args
			}
			id := inst.ID
			if id == "" {
				id = fmt.Sprintf("%s-%d", spec.Name, i)
			}
			out = append(out, &ManagedProcess{ID: id, Service: spec.Name, Spec: spec, Args: append([]string(nil), args...)})
		}
		return out
	}
	return []*ManagedProcess{{ID: spec.Name, Service: spec.Name, Spec: spec, Args: append([]string(nil), spec.Args...)}}
}

func isPIDAlive(pid int) (bool, error) {
	if pid <= 0 {
		return false, nil
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	return false, err
}
