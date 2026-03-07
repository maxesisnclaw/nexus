package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/maxesisn/nexus/pkg/config"
)

// ProcessState describes a managed process state snapshot.
type ProcessState struct {
	// ID is the unique managed process identifier.
	ID string
	// Service is the owning service name.
	Service string
	// PID is the OS process id for binary runtime, or 0 for non-process runtimes.
	PID int
	// Running reports whether the managed instance is currently alive.
	Running bool
}

// ManagedProcess keeps runtime state for one process instance.
type ManagedProcess struct {
	// ID is the unique managed process identifier.
	ID string
	// Service is the owning service name.
	Service string
	// Spec is the configured service specification.
	Spec config.ServiceSpec
	// Args contains effective runtime args after instance expansion.
	Args []string

	cmd           *exec.Cmd
	containerName string
	exited        chan struct{}
}

type prefixWriter struct {
	mu     sync.Mutex
	logger *slog.Logger
	id     string
	stream string
	buf    []byte
}

const maxLineLength = 64 * 1024

func (w *prefixWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	w.flushLocked(false)
	if len(w.buf) > maxLineLength {
		w.flushLocked(true)
	}
	return len(p), nil
}

func (w *prefixWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.flushLocked(true)
}

func (w *prefixWriter) flushLocked(flushPartial bool) {
	for {
		idx := bytes.IndexByte(w.buf, '\n')
		if idx < 0 {
			break
		}
		line := string(w.buf[:idx])
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		w.logLine(line)
		w.buf = w.buf[idx+1:]
	}
	if flushPartial && len(w.buf) > 0 {
		line := string(w.buf)
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		w.logLine(line)
		w.buf = w.buf[:0]
	}
}

func (w *prefixWriter) logLine(line string) {
	if w.stream == "stderr" {
		w.logger.Warn("process output", "id", w.id, "stream", w.stream, "line", line)
		return
	}
	w.logger.Info("process output", "id", w.id, "stream", w.stream, "line", line)
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
			var rollbackErr error
			for i := len(startedIDs) - 1; i >= 0; i-- {
				if stopErr := m.StopProcess(startedIDs[i]); stopErr != nil {
					rollbackErr = errors.Join(rollbackErr, fmt.Errorf("rollback stop %s: %w", startedIDs[i], stopErr))
				}
			}
			if rollbackErr != nil {
				m.logger.Warn("startup rollback incomplete", "err", rollbackErr)
			}
			return errors.Join(err, rollbackErr)
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

	pgid, err := syscall.Getpgid(proc.cmd.Process.Pid)
	if err != nil {
		pgid = proc.cmd.Process.Pid
	}

	if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
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
		m.deleteProcessIfMatch(id, proc)
		return nil
	case <-timer.C:
	}

	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("signal SIGKILL %s: %w", id, err)
	}
	select {
	case <-waitCh:
		m.deleteProcessIfMatch(id, proc)
		return nil
	case <-time.After(500 * time.Millisecond):
		m.logger.Warn("process did not exit after SIGKILL, keeping record for retry", "id", id)
		return fmt.Errorf("process %s did not exit after SIGKILL", id)
	}
}

// States returns a snapshot of all process states.
func (m *ProcessManager) States() []ProcessState {
	type snapshot struct {
		id            string
		service       string
		runtime       string
		containerName string
		pid           int
		cmd           *exec.Cmd
	}

	m.mu.RLock()
	snaps := make([]snapshot, 0, len(m.procs))
	for id, p := range m.procs {
		s := snapshot{
			id:            id,
			service:       p.Service,
			runtime:       p.Spec.Runtime,
			containerName: p.containerName,
		}
		if p.cmd != nil && p.cmd.Process != nil {
			s.pid = p.cmd.Process.Pid
			s.cmd = p.cmd
		}
		snaps = append(snaps, s)
	}
	m.mu.RUnlock()

	states := make([]ProcessState, 0, len(snaps))
	for _, s := range snaps {
		running := false
		if s.runtime == "docker" {
			alive, err := m.docker.IsRunning(s.containerName)
			running = err == nil && alive
		} else if s.cmd != nil && s.cmd.Process != nil {
			alive, err := isPIDAlive(s.pid)
			running = err == nil && alive
		}
		states = append(states, ProcessState{ID: s.id, Service: s.service, PID: s.pid, Running: running})
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
	reservation := &ManagedProcess{
		ID:      proc.ID,
		Service: proc.Service,
		Spec:    proc.Spec,
		Args:    append([]string(nil), proc.Args...),
	}

	m.mu.Lock()
	if _, exists := m.procs[proc.ID]; exists {
		m.mu.Unlock()
		return fmt.Errorf("process %s already exists", proc.ID)
	}
	m.procs[proc.ID] = reservation
	m.mu.Unlock()

	if proc.Spec.Runtime == "docker" {
		containerName, err := m.docker.Start(ctx, proc)
		if err != nil {
			m.deleteProcessIfMatch(proc.ID, reservation)
			return fmt.Errorf("start docker %s: %w", proc.ID, err)
		}
		proc.containerName = containerName

		m.mu.Lock()
		current, ok := m.procs[proc.ID]
		if ok && current == reservation {
			m.procs[proc.ID] = proc
		}
		m.mu.Unlock()
		if !ok || current != reservation {
			_ = m.docker.Stop(containerName, m.stopWait)
			return fmt.Errorf("start process %s canceled during startup", proc.ID)
		}

		m.logger.Info("service process started", "id", proc.ID, "service", proc.Service, "runtime", "docker", "container", containerName)
		return nil
	}
	if proc.Spec.Runtime != "binary" {
		m.deleteProcessIfMatch(proc.ID, reservation)
		return fmt.Errorf("service %s runtime %s not supported in process manager", proc.Service, proc.Spec.Runtime)
	}

	cmd := exec.Command(proc.Spec.Binary, proc.Args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdoutWriter := &prefixWriter{logger: m.logger, id: proc.ID, stream: "stdout"}
	stderrWriter := &prefixWriter{logger: m.logger, id: proc.ID, stream: "stderr"}
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter
	if err := cmd.Start(); err != nil {
		m.deleteProcessIfMatch(proc.ID, reservation)
		return fmt.Errorf("start process %s: %w", proc.ID, err)
	}
	proc.cmd = cmd
	proc.exited = make(chan struct{})

	m.mu.Lock()
	current, ok := m.procs[proc.ID]
	if ok && current == reservation {
		m.procs[proc.ID] = proc
	}
	m.mu.Unlock()
	if !ok || current != reservation {
		if pgid, err := syscall.Getpgid(proc.cmd.Process.Pid); err == nil {
			_ = syscall.Kill(-pgid, syscall.SIGTERM)
		} else {
			_ = proc.cmd.Process.Signal(syscall.SIGTERM)
		}
		time.Sleep(100 * time.Millisecond)
		if pgid, err := syscall.Getpgid(proc.cmd.Process.Pid); err == nil {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		} else {
			_ = proc.cmd.Process.Signal(syscall.SIGKILL)
		}
		_ = cmd.Wait()
		stdoutWriter.Flush()
		stderrWriter.Flush()
		return fmt.Errorf("start process %s canceled during startup", proc.ID)
	}

	m.logger.Info("service process started", "id", proc.ID, "service", proc.Service, "pid", cmd.Process.Pid)
	go func(p *ManagedProcess, c *exec.Cmd, exited chan struct{}, outw, errw *prefixWriter) {
		err := c.Wait()
		outw.Flush()
		errw.Flush()
		close(exited)
		if err != nil {
			m.logger.Warn("service process exited with error", "id", p.ID, "err", err)
		} else {
			m.logger.Info("service process exited", "id", p.ID)
		}
		m.deleteProcessIfMatch(p.ID, p)
	}(proc, cmd, proc.exited, stdoutWriter, stderrWriter)
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
