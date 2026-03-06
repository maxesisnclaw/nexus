package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"nexus/pkg/config"
)

func TestProcessManagerStartStopService(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	pm := NewProcessManager(logger, 2*time.Second)
	spec := config.ServiceSpec{
		Name:    "sleepy",
		Type:    "singleton",
		Runtime: "binary",
		Binary:  "/bin/sleep",
		Args:    []string{"30"},
	}

	if err := pm.StartService(context.Background(), spec); err != nil {
		t.Fatalf("StartService() error = %v", err)
	}
	if !pm.IsRunning("sleepy") {
		t.Fatal("expected process to be running")
	}
	if err := pm.StopService("sleepy"); err != nil {
		t.Fatalf("StopService() error = %v", err)
	}
	if pm.IsRunning("sleepy") {
		t.Fatal("expected process to be stopped")
	}
}

func TestDaemonStartStop(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cfg := &config.Config{
		Daemon: config.DaemonConfig{
			HealthInterval: config.Duration{Duration: 100 * time.Millisecond},
			ShutdownGrace:  config.Duration{Duration: 2 * time.Second},
		},
		Services: []config.ServiceSpec{{
			Name:    "sleepy",
			Type:    "singleton",
			Runtime: "binary",
			Binary:  "/bin/sleep",
			Args:    []string{"30"},
		}},
	}

	d := New(cfg, logger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := d.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	time.Sleep(120 * time.Millisecond)
	states := d.ProcessStates()
	if len(states) != 1 || !states[0].Running {
		t.Fatalf("unexpected states: %+v", states)
	}

	if err := d.Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
}

func TestProcessManagerDockerRuntime(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	pm := NewProcessManager(logger, 2*time.Second)
	fake := &fakeDockerRuntime{running: map[string]bool{}}
	pm.docker = fake

	spec := config.ServiceSpec{
		Name:    "legacy",
		Type:    "singleton",
		Runtime: "docker",
		Image:   "repo/legacy:latest",
		Args:    []string{"--mode", "test"},
	}
	if err := pm.StartService(context.Background(), spec); err != nil {
		t.Fatalf("StartService() error = %v", err)
	}
	if !pm.IsRunning("legacy") {
		t.Fatal("expected docker service running")
	}
	if err := pm.StopService("legacy"); err != nil {
		t.Fatalf("StopService() error = %v", err)
	}
	if pm.IsRunning("legacy") {
		t.Fatal("expected docker service stopped")
	}
}

func TestDaemonStartAlreadyStartedAndStopNoop(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cfg := &config.Config{
		Daemon: config.DaemonConfig{
			HealthInterval: config.Duration{Duration: 100 * time.Millisecond},
			ShutdownGrace:  config.Duration{Duration: 1 * time.Second},
		},
	}

	d := New(cfg, logger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := d.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := d.Start(ctx); err == nil {
		t.Fatal("expected second Start() to fail")
	}
	if err := d.Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := d.Stop(); err != nil {
		t.Fatalf("second Stop() error = %v", err)
	}
}

func TestDaemonStartFailureResetsState(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cfg := &config.Config{
		Daemon: config.DaemonConfig{
			HealthInterval: config.Duration{Duration: 100 * time.Millisecond},
			ShutdownGrace:  config.Duration{Duration: 1 * time.Second},
		},
		Services: []config.ServiceSpec{{
			Name:    "bad",
			Runtime: "binary",
			Binary:  "/path/does/not/exist",
		}},
	}

	d := New(cfg, logger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := d.Start(ctx); err == nil {
		t.Fatal("expected Start() failure")
	}
	if err := d.Start(ctx); err == nil {
		t.Fatal("expected Start() to remain failing but not be stuck started")
	}
}

func TestDaemonRestartServiceNotFound(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	d := New(&config.Config{
		Daemon: config.DaemonConfig{
			HealthInterval: config.Duration{Duration: 100 * time.Millisecond},
			ShutdownGrace:  config.Duration{Duration: 1 * time.Second},
		},
	}, logger)
	err := d.RestartService(context.Background(), "missing")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not found error, got %v", err)
	}
}

func TestProcessManagerRollsBackOnDuplicateInstanceID(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	pm := NewProcessManager(logger, time.Second)
	spec := config.ServiceSpec{
		Name:    "dup",
		Type:    "worker",
		Runtime: "binary",
		Binary:  "/bin/sleep",
		Args:    []string{"30"},
		Instances: []config.InstanceSpec{
			{ID: "dup-1"},
			{ID: "dup-1"},
		},
	}

	if err := pm.StartService(context.Background(), spec); err == nil {
		t.Fatal("expected StartService() to fail on duplicate process ID")
	}
	if states := pm.States(); len(states) != 0 {
		t.Fatalf("expected rollback cleanup, got states=%+v", states)
	}
}

func TestProcessManagerStartProcessRejectsConcurrentDuplicateID(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	pm := NewProcessManager(logger, time.Second)
	procSpec := config.ServiceSpec{
		Name:    "dup-concurrent",
		Type:    "singleton",
		Runtime: "binary",
		Binary:  "/bin/sleep",
		Args:    []string{"30"},
	}

	first := &ManagedProcess{ID: "dup-concurrent", Service: "dup-concurrent", Spec: procSpec, Args: []string{"30"}}
	second := &ManagedProcess{ID: "dup-concurrent", Service: "dup-concurrent", Spec: procSpec, Args: []string{"30"}}

	var wg sync.WaitGroup
	wg.Add(2)
	results := make(chan error, 2)
	go func() {
		defer wg.Done()
		results <- pm.startProcess(context.Background(), first)
	}()
	go func() {
		defer wg.Done()
		results <- pm.startProcess(context.Background(), second)
	}()
	wg.Wait()
	close(results)

	var success, failures int
	for err := range results {
		if err == nil {
			success++
			continue
		}
		if !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("unexpected startProcess() error: %v", err)
		}
		failures++
	}
	if success != 1 || failures != 1 {
		t.Fatalf("expected one success and one duplicate failure, got success=%d failures=%d", success, failures)
	}
	if err := pm.StopProcess("dup-concurrent"); err != nil {
		t.Fatalf("StopProcess() error = %v", err)
	}
}

func TestProcessManagerCleansExitedProcess(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	pm := NewProcessManager(logger, time.Second)
	spec := config.ServiceSpec{
		Name:    "oneshot",
		Type:    "singleton",
		Runtime: "binary",
		Binary:  "/bin/sh",
		Args:    []string{"-c", "exit 0"},
	}

	if err := pm.StartService(context.Background(), spec); err != nil {
		t.Fatalf("StartService() error = %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(pm.States()) == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("expected process map cleanup, states=%+v", pm.States())
}

func TestExpandInstancesDefaults(t *testing.T) {
	spec := config.ServiceSpec{
		Name:    "worker",
		Type:    "worker",
		Runtime: "binary",
		Binary:  "/bin/echo",
		Args:    []string{"default"},
		Instances: []config.InstanceSpec{
			{ID: "", Args: nil},
			{ID: "w2", Args: []string{"override"}},
		},
	}
	items := expandInstances(spec)
	if len(items) != 2 {
		t.Fatalf("unexpected instance count: %d", len(items))
	}
	if items[0].ID != "worker-0" {
		t.Fatalf("unexpected generated ID: %s", items[0].ID)
	}
	if got := strings.Join(items[0].Args, ","); got != "default" {
		t.Fatalf("unexpected default args: %s", got)
	}
	if got := strings.Join(items[1].Args, ","); got != "override" {
		t.Fatalf("unexpected override args: %s", got)
	}
}

func TestIsPIDAliveInvalidAndCurrentProcess(t *testing.T) {
	alive, err := isPIDAlive(-1)
	if err != nil || alive {
		t.Fatalf("expected invalid pid to be not alive without error, got alive=%v err=%v", alive, err)
	}
	cmd := exec.Command("/bin/sleep", "1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	defer cmd.Process.Kill()
	alive, err = isPIDAlive(cmd.Process.Pid)
	if err != nil || !alive {
		t.Fatalf("expected running process alive, got alive=%v err=%v", alive, err)
	}
}

func TestNewHealthMonitorDefaultInterval(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	pm := NewProcessManager(logger, time.Second)
	h := NewHealthMonitor(logger, pm, 0)
	if h.interval <= 0 {
		t.Fatalf("expected default interval, got %s", h.interval)
	}
}

func TestHealthMonitorRestartsUnhealthyProcess(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	pm := NewProcessManager(logger, time.Second)
	pm.docker = &fakeDockerRuntime{
		running: map[string]bool{
			"nexus-worker": false,
		},
	}
	pm.procs["worker"] = &ManagedProcess{
		ID:            "worker",
		Service:       "worker",
		Spec:          config.ServiceSpec{Name: "worker", Runtime: "docker", Image: "repo/worker:latest"},
		containerName: "nexus-worker",
	}
	h := NewHealthMonitor(logger, pm, 100*time.Millisecond)

	h.checkOnce(context.Background())

	if !pm.IsRunning("worker") {
		t.Fatal("expected unhealthy process to be restarted")
	}
}

func TestProcessManagerStopServiceAggregatesErrors(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	pm := NewProcessManager(logger, time.Second)
	pm.docker = &fakeDockerRuntime{
		running: map[string]bool{
			"nexus-a": true,
			"nexus-b": true,
		},
		stopErrs: map[string]error{
			"nexus-a": errors.New("stop a failed"),
			"nexus-b": errors.New("stop b failed"),
		},
	}
	pm.procs["a"] = &ManagedProcess{ID: "a", Service: "svc", Spec: config.ServiceSpec{Runtime: "docker"}, containerName: "nexus-a"}
	pm.procs["b"] = &ManagedProcess{ID: "b", Service: "svc", Spec: config.ServiceSpec{Runtime: "docker"}, containerName: "nexus-b"}

	err := pm.StopService("svc")
	if err == nil {
		t.Fatal("expected aggregated stop error")
	}
	if !strings.Contains(err.Error(), "stop a failed") || !strings.Contains(err.Error(), "stop b failed") {
		t.Fatalf("unexpected aggregated error: %v", err)
	}
}

type fakeDockerRuntime struct {
	running  map[string]bool
	stopErrs map[string]error
}

func (f *fakeDockerRuntime) Start(_ context.Context, proc *ManagedProcess) (string, error) {
	name := "nexus-" + proc.ID
	f.running[name] = true
	return name, nil
}

func (f *fakeDockerRuntime) Stop(container string, _ time.Duration) error {
	if err := f.stopErrs[container]; err != nil {
		return err
	}
	delete(f.running, container)
	return nil
}

func (f *fakeDockerRuntime) IsRunning(container string) (bool, error) {
	return f.running[container], nil
}
