package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maxesisn/nexus/pkg/config"
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

func TestDaemonStopPreservesSIGTERMHandling(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	marker := filepath.Join(t.TempDir(), "sigterm.marker")
	cfg := &config.Config{
		Daemon: config.DaemonConfig{
			HealthInterval: config.Duration{Duration: 100 * time.Millisecond},
			ShutdownGrace:  config.Duration{Duration: 2 * time.Second},
		},
		Services: []config.ServiceSpec{{
			Name:    "trapped",
			Type:    "singleton",
			Runtime: "binary",
			Binary:  "/bin/sh",
			Args: []string{
				"-c",
				fmt.Sprintf(`trap 'sleep 0.2; echo term > %q; exit 0' TERM; while true; do sleep 1; done`, marker),
			},
		}},
	}

	d := New(cfg, logger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := d.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	running := false
	for time.Now().Before(deadline) {
		states := d.ProcessStates()
		if len(states) == 1 && states[0].Running {
			running = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !running {
		t.Fatalf("expected managed process to be running, got states=%+v", d.ProcessStates())
	}
	time.Sleep(100 * time.Millisecond)

	if err := d.Stop(); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("expected SIGTERM marker file to be written: %v", err)
	}
	if strings.TrimSpace(string(data)) != "term" {
		t.Fatalf("unexpected marker content: %q", string(data))
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
	h := NewHealthMonitor(logger, pm, 0, 0)
	if h.interval <= 0 {
		t.Fatalf("expected default interval, got %s", h.interval)
	}
	if h.maxRestarts != 5 {
		t.Fatalf("expected default max restarts 5, got %d", h.maxRestarts)
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
	h := NewHealthMonitor(logger, pm, 100*time.Millisecond, 5)

	h.checkOnce(context.Background())

	if !pm.IsRunning("worker") {
		t.Fatal("expected unhealthy process to be restarted")
	}
}

func TestHealthMonitorCleansStaleRestartEntries(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	pm := NewProcessManager(logger, time.Second)
	h := NewHealthMonitor(logger, pm, 100*time.Millisecond, 5)
	h.restartState["stale"] = restartState{consecutiveFailures: 1, lastAttempt: time.Now().Add(-time.Minute)}

	h.checkOnce(context.Background())

	if len(h.restartState) != 0 {
		t.Fatalf("expected stale restart map to be cleaned, got %+v", h.restartState)
	}
}

func TestHealthMonitorKeepsRestartStateForActiveProcess(t *testing.T) {
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

	h := NewHealthMonitor(logger, pm, time.Second, 5)
	h.restartState["worker"] = restartState{consecutiveFailures: 1, lastAttempt: time.Now()}
	h.restartState["stale"] = restartState{consecutiveFailures: 1, lastAttempt: time.Now().Add(-time.Minute)}

	h.checkOnce(context.Background())

	if _, ok := h.restartState["stale"]; ok {
		t.Fatalf("expected stale restart entry to be removed, got %+v", h.restartState)
	}
	if _, ok := h.restartState["worker"]; !ok {
		t.Fatalf("expected active restart entry to be preserved, got %+v", h.restartState)
	}
	if pm.IsRunning("worker") {
		t.Fatal("expected restart throttle to prevent immediate restart")
	}
}

func TestHealthMonitorExponentialBackoff(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	pm := NewProcessManager(logger, time.Second)
	h := NewHealthMonitor(logger, pm, time.Second, 5)

	now := time.Unix(1000, 0)
	if !h.shouldRestart("worker", now) {
		t.Fatal("expected first restart attempt to be allowed")
	}
	if h.shouldRestart("worker", now.Add(500*time.Millisecond)) {
		t.Fatal("expected second attempt to back off for at least base interval")
	}
	if !h.shouldRestart("worker", now.Add(1*time.Second)) {
		t.Fatal("expected second attempt after base interval")
	}
	if h.shouldRestart("worker", now.Add(2500*time.Millisecond)) {
		t.Fatal("expected third attempt to wait for doubled backoff")
	}
	if !h.shouldRestart("worker", now.Add(3*time.Second)) {
		t.Fatal("expected third attempt after doubled backoff")
	}

	state, ok := h.restartState["worker"]
	if !ok || state.consecutiveFailures != 3 {
		t.Fatalf("unexpected restart state: %+v", state)
	}
}

func TestHealthMonitorMaxRestartLimit(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	pm := NewProcessManager(logger, time.Second)
	pm.docker = &fakeDockerRuntime{
		running: map[string]bool{
			"nexus-worker": false,
		},
		keepDown: true,
	}
	pm.procs["worker"] = &ManagedProcess{
		ID:            "worker",
		Service:       "worker",
		Spec:          config.ServiceSpec{Name: "worker", Runtime: "docker", Image: "repo/worker:latest"},
		containerName: "nexus-worker",
	}

	h := NewHealthMonitor(logger, pm, 100*time.Millisecond, 3)
	h.baseBackoff = 0
	for i := 0; i < 6; i++ {
		h.checkOnce(context.Background())
	}

	fake := pm.docker.(*fakeDockerRuntime)
	if fake.startCalls != 3 {
		t.Fatalf("expected exactly 3 restart attempts, got %d", fake.startCalls)
	}
	state, ok := h.restartState["worker"]
	if !ok || !state.exhausted {
		t.Fatalf("expected exhausted restart state, got %+v", state)
	}
}

func TestHealthMonitorResetsOnHealthy(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	pm := NewProcessManager(logger, time.Second)
	fake := &fakeDockerRuntime{
		running: map[string]bool{
			"nexus-worker": false,
		},
		keepDown: true,
	}
	pm.docker = fake
	pm.procs["worker"] = &ManagedProcess{
		ID:            "worker",
		Service:       "worker",
		Spec:          config.ServiceSpec{Name: "worker", Runtime: "docker", Image: "repo/worker:latest"},
		containerName: "nexus-worker",
	}

	h := NewHealthMonitor(logger, pm, 100*time.Millisecond, 5)
	h.baseBackoff = 0

	h.checkOnce(context.Background())
	state, ok := h.restartState["worker"]
	if !ok || state.consecutiveFailures != 1 {
		t.Fatalf("expected one recorded failure, got %+v", state)
	}

	fake.keepDown = false
	fake.running["nexus-worker"] = true
	h.checkOnce(context.Background())
	if _, ok := h.restartState["worker"]; ok {
		t.Fatalf("expected restart state reset after healthy check, got %+v", h.restartState["worker"])
	}

	fake.keepDown = true
	fake.running["nexus-worker"] = false
	h.checkOnce(context.Background())
	state, ok = h.restartState["worker"]
	if !ok || state.consecutiveFailures != 1 {
		t.Fatalf("expected failure counter reset to 1 after healthy cycle, got %+v", state)
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

func TestProcessManagerRestartServiceDocker(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	pm := NewProcessManager(logger, time.Second)
	fake := &fakeDockerRuntime{running: map[string]bool{}}
	pm.docker = fake

	spec := config.ServiceSpec{
		Name:    "legacy",
		Type:    "singleton",
		Runtime: "docker",
		Image:   "repo/legacy:latest",
	}
	if err := pm.StartService(context.Background(), spec); err != nil {
		t.Fatalf("StartService() error = %v", err)
	}
	if fake.startCalls != 1 {
		t.Fatalf("expected first start call, got %d", fake.startCalls)
	}

	if err := pm.RestartService(context.Background(), spec); err != nil {
		t.Fatalf("RestartService() error = %v", err)
	}
	if fake.startCalls != 2 {
		t.Fatalf("expected restart to start service again, got %d starts", fake.startCalls)
	}
	if !pm.IsRunning("legacy") {
		t.Fatal("expected restarted service to be running")
	}
}

func TestProcessManagerRestartServiceStopsOnStopError(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	pm := NewProcessManager(logger, time.Second)
	fake := &fakeDockerRuntime{running: map[string]bool{}}
	pm.docker = fake

	spec := config.ServiceSpec{
		Name:    "legacy",
		Type:    "singleton",
		Runtime: "docker",
		Image:   "repo/legacy:latest",
	}
	if err := pm.StartService(context.Background(), spec); err != nil {
		t.Fatalf("StartService() error = %v", err)
	}
	fake.stopErrs = map[string]error{
		"nexus-legacy": errors.New("stop failed"),
	}

	err := pm.RestartService(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), "stop failed") {
		t.Fatalf("expected restart stop error, got %v", err)
	}
	if fake.startCalls != 1 {
		t.Fatalf("expected restart to abort before re-start, got %d starts", fake.startCalls)
	}
}

func TestProcessManagerRestartProcessNotFound(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	pm := NewProcessManager(logger, time.Second)

	err := pm.RestartProcess(context.Background(), "missing")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected process not found error, got %v", err)
	}
}

func TestProcessManagerRestartProcessNilContext(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	pm := NewProcessManager(logger, time.Second)
	fake := &fakeDockerRuntime{running: map[string]bool{}}
	pm.docker = fake

	spec := config.ServiceSpec{
		Name:    "legacy",
		Type:    "singleton",
		Runtime: "docker",
		Image:   "repo/legacy:latest",
	}
	if err := pm.StartService(context.Background(), spec); err != nil {
		t.Fatalf("StartService() error = %v", err)
	}

	if err := pm.RestartProcess(nil, "legacy"); err != nil {
		t.Fatalf("RestartProcess() error = %v", err)
	}
	if fake.startCalls != 2 {
		t.Fatalf("expected one start and one restart start, got %d", fake.startCalls)
	}
	if !pm.IsRunning("legacy") {
		t.Fatal("expected restarted process to be running")
	}
}

func TestProcessManagerRestartProcessStopError(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	pm := NewProcessManager(logger, time.Second)
	fake := &fakeDockerRuntime{running: map[string]bool{}}
	pm.docker = fake

	spec := config.ServiceSpec{
		Name:    "legacy",
		Type:    "singleton",
		Runtime: "docker",
		Image:   "repo/legacy:latest",
	}
	if err := pm.StartService(context.Background(), spec); err != nil {
		t.Fatalf("StartService() error = %v", err)
	}
	fake.stopErrs = map[string]error{
		"nexus-legacy": errors.New("stop failed"),
	}

	err := pm.RestartProcess(context.Background(), "legacy")
	if err == nil || !strings.Contains(err.Error(), "stop failed") {
		t.Fatalf("expected restart process stop error, got %v", err)
	}
	if fake.startCalls != 1 {
		t.Fatalf("expected no extra start on failed restart, got %d starts", fake.startCalls)
	}
}

type fakeDockerRuntime struct {
	running    map[string]bool
	stopErrs   map[string]error
	keepDown   bool
	startCalls int
}

func (f *fakeDockerRuntime) Start(_ context.Context, proc *ManagedProcess) (string, error) {
	name := "nexus-" + proc.ID
	f.startCalls++
	if !f.keepDown {
		f.running[name] = true
	}
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
