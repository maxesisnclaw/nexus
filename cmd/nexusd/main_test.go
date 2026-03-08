package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/maxesisn/nexus/pkg/config"
)

func TestNewLoggerLevelMapping(t *testing.T) {
	tests := []struct {
		name    string
		level   string
		debugOn bool
		errorOn bool
	}{
		{name: "debug", level: "debug", debugOn: true, errorOn: true},
		{name: "warn", level: "warn", debugOn: false, errorOn: true},
		{name: "error", level: "error", debugOn: false, errorOn: true},
		{name: "default", level: "unknown", debugOn: false, errorOn: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logger := newLogger(tc.level)
			ctx := context.Background()
			if got := logger.Enabled(ctx, slog.LevelDebug); got != tc.debugOn {
				t.Fatalf("debug enabled mismatch: got=%v want=%v", got, tc.debugOn)
			}
			if got := logger.Enabled(ctx, slog.LevelError); got != tc.errorOn {
				t.Fatalf("error enabled mismatch: got=%v want=%v", got, tc.errorOn)
			}
		})
	}
}

type stubDaemonRunner struct {
	startErr error
	stopErr  error
	onStart  func(context.Context)

	startCalls int
	stopCalls  int
}

func (s *stubDaemonRunner) Start(ctx context.Context) error {
	s.startCalls++
	if s.onStart != nil {
		s.onStart(ctx)
	}
	return s.startErr
}

func (s *stubDaemonRunner) Stop() error {
	s.stopCalls++
	return s.stopErr
}

func TestRunParseError(t *testing.T) {
	code := run([]string{"-invalid"})
	if code != 2 {
		t.Fatalf("run() code = %d, want 2", code)
	}
}

func TestRunLoadConfigFailure(t *testing.T) {
	origLoad := loadConfig
	origNewDaemon := newDaemon
	origNotify := notifyContext
	t.Cleanup(func() {
		loadConfig = origLoad
		newDaemon = origNewDaemon
		notifyContext = origNotify
	})

	loadConfig = func(string) (*config.Config, error) {
		return nil, errors.New("boom")
	}

	code := run(nil)
	if code != 1 {
		t.Fatalf("run() code = %d, want 1", code)
	}
}

func TestRunStartFailure(t *testing.T) {
	origLoad := loadConfig
	origNewDaemon := newDaemon
	origNotify := notifyContext
	t.Cleanup(func() {
		loadConfig = origLoad
		newDaemon = origNewDaemon
		notifyContext = origNotify
	})

	cfg := &config.Config{Daemon: config.DaemonConfig{LogLevel: "info"}}
	stub := &stubDaemonRunner{startErr: errors.New("start failed")}

	loadConfig = func(string) (*config.Config, error) {
		return cfg, nil
	}
	newDaemon = func(*config.Config, *slog.Logger) (daemonRunner, error) {
		return stub, nil
	}
	notifyContext = func(context.Context, ...os.Signal) (context.Context, context.CancelFunc) {
		return context.WithCancel(context.Background())
	}

	code := run(nil)
	if code != 1 {
		t.Fatalf("run() code = %d, want 1", code)
	}
	if stub.startCalls != 1 {
		t.Fatalf("Start() calls = %d, want 1", stub.startCalls)
	}
	if stub.stopCalls != 0 {
		t.Fatalf("Stop() calls = %d, want 0", stub.stopCalls)
	}
}

func TestRunStartAndStopLifecycle(t *testing.T) {
	origLoad := loadConfig
	origNewDaemon := newDaemon
	origNotify := notifyContext
	t.Cleanup(func() {
		loadConfig = origLoad
		newDaemon = origNewDaemon
		notifyContext = origNotify
	})

	cfg := &config.Config{Daemon: config.DaemonConfig{LogLevel: "info"}}
	var cancel context.CancelFunc
	stub := &stubDaemonRunner{
		onStart: func(context.Context) {
			cancel()
		},
	}

	loadConfig = func(string) (*config.Config, error) {
		return cfg, nil
	}
	newDaemon = func(*config.Config, *slog.Logger) (daemonRunner, error) {
		return stub, nil
	}
	notifyContext = func(parent context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
		ctx, c := context.WithCancel(parent)
		cancel = c
		return ctx, c
	}

	code := run(nil)
	if code != 0 {
		t.Fatalf("run() code = %d, want 0", code)
	}
	if stub.startCalls != 1 {
		t.Fatalf("Start() calls = %d, want 1", stub.startCalls)
	}
	if stub.stopCalls != 1 {
		t.Fatalf("Stop() calls = %d, want 1", stub.stopCalls)
	}
}

func TestRunStopCanceledAllowed(t *testing.T) {
	origLoad := loadConfig
	origNewDaemon := newDaemon
	origNotify := notifyContext
	t.Cleanup(func() {
		loadConfig = origLoad
		newDaemon = origNewDaemon
		notifyContext = origNotify
	})

	cfg := &config.Config{Daemon: config.DaemonConfig{LogLevel: "info"}}
	var cancel context.CancelFunc
	stub := &stubDaemonRunner{
		stopErr: context.Canceled,
		onStart: func(context.Context) {
			cancel()
		},
	}

	loadConfig = func(string) (*config.Config, error) {
		return cfg, nil
	}
	newDaemon = func(*config.Config, *slog.Logger) (daemonRunner, error) {
		return stub, nil
	}
	notifyContext = func(parent context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
		ctx, c := context.WithCancel(parent)
		cancel = c
		return ctx, c
	}

	code := run(nil)
	if code != 0 {
		t.Fatalf("run() code = %d, want 0", code)
	}
}

func TestRunStopFailure(t *testing.T) {
	origLoad := loadConfig
	origNewDaemon := newDaemon
	origNotify := notifyContext
	t.Cleanup(func() {
		loadConfig = origLoad
		newDaemon = origNewDaemon
		notifyContext = origNotify
	})

	cfg := &config.Config{Daemon: config.DaemonConfig{LogLevel: "info"}}
	var cancel context.CancelFunc
	stub := &stubDaemonRunner{
		stopErr: errors.New("stop failed"),
		onStart: func(context.Context) {
			cancel()
		},
	}

	loadConfig = func(string) (*config.Config, error) {
		return cfg, nil
	}
	newDaemon = func(*config.Config, *slog.Logger) (daemonRunner, error) {
		return stub, nil
	}
	notifyContext = func(parent context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
		ctx, c := context.WithCancel(parent)
		cancel = c
		return ctx, c
	}

	code := run(nil)
	if code != 1 {
		t.Fatalf("run() code = %d, want 1", code)
	}
	if stub.stopCalls != 1 {
		t.Fatalf("Stop() calls = %d, want 1", stub.stopCalls)
	}
}

func TestRunValidateValidConfig(t *testing.T) {
	configPath := writeTempConfig(t, `
[daemon]
socket = "/tmp/nexus.sock"

[[service]]
name = "api"
type = "singleton"
runtime = "binary"
binary = "/bin/true"

[[service]]
name = "worker"
type = "singleton"
runtime = "binary"
binary = "/bin/true"
depends_on = ["api"]
`)

	code, out, _ := runWithCapturedOutput(t, []string{"validate", "-config", configPath})
	if code != 0 {
		t.Fatalf("run(validate) code = %d, want 0", code)
	}
	if !strings.Contains(out, "config valid") {
		t.Fatalf("validate output missing success message: %q", out)
	}
}

func TestRunValidateInvalidConfig(t *testing.T) {
	configPath := writeTempConfig(t, `
[daemon]
socket = "/tmp/nexus.sock"

[[service]]
type = "singleton"
runtime = "binary"
binary = "/bin/true"
`)

	code, out, _ := runWithCapturedOutput(t, []string{"validate", "-config", configPath})
	if code != 1 {
		t.Fatalf("run(validate) code = %d, want 1", code)
	}
	if !strings.Contains(out, "config invalid") {
		t.Fatalf("validate output missing error message: %q", out)
	}
}

func TestRunValidateCircularDependency(t *testing.T) {
	configPath := writeTempConfig(t, `
[daemon]
socket = "/tmp/nexus.sock"

[[service]]
name = "api"
type = "singleton"
runtime = "binary"
binary = "/bin/true"
depends_on = ["worker"]

[[service]]
name = "worker"
type = "singleton"
runtime = "binary"
binary = "/bin/true"
depends_on = ["api"]
`)

	code, out, _ := runWithCapturedOutput(t, []string{"validate", "-config", configPath})
	if code != 1 {
		t.Fatalf("run(validate) code = %d, want 1", code)
	}
	if !strings.Contains(strings.ToLower(out), "circular") {
		t.Fatalf("validate output missing circular dependency message: %q", out)
	}
}

func TestRunKeygen(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "nexus.key")

	code, out, _ := runWithCapturedOutput(t, []string{"keygen", "-out", outPath})
	if code != 0 {
		t.Fatalf("run(keygen) code = %d, want 0", code)
	}
	if !strings.Contains(out, "public key:") {
		t.Fatalf("keygen output missing public key: %q", out)
	}
	if !strings.Contains(out, "private key written to: "+outPath) {
		t.Fatalf("keygen output missing private key path: %q", out)
	}

	matches := regexp.MustCompile(`public key:\s*([0-9a-f]{64})`).FindStringSubmatch(out)
	if len(matches) != 2 {
		t.Fatalf("public key format mismatch: %q", out)
	}
	if _, err := hex.DecodeString(matches[1]); err != nil {
		t.Fatalf("public key is not valid hex: %v", err)
	}

	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("stat private key file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("private key file mode = %o, want 600", info.Mode().Perm())
	}

	content, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read private key file: %v", err)
	}
	raw := strings.TrimSpace(string(content))
	if len(raw) != 64 {
		t.Fatalf("private key length = %d, want 64 hex chars", len(raw))
	}
	if _, err := hex.DecodeString(raw); err != nil {
		t.Fatalf("private key is not valid hex: %v", err)
	}
}

func TestRunKeygenHardensExistingFilePermissions(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "nexus.key")
	if err := os.WriteFile(outPath, []byte("insecure"), 0o644); err != nil {
		t.Fatalf("seed insecure key file: %v", err)
	}
	if err := os.Chmod(outPath, 0o644); err != nil {
		t.Fatalf("chmod insecure key file: %v", err)
	}

	code, _, errOut := runWithCapturedOutput(t, []string{"keygen", "-out", outPath})
	if code != 0 {
		t.Fatalf("run(keygen) code = %d, want 0 (stderr=%q)", code, errOut)
	}

	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("stat private key file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("private key file mode = %o, want 600", info.Mode().Perm())
	}
}

func TestRunStatusNoDaemon(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "registry.sock")

	code, _, errOut := runWithCapturedOutput(t, []string{"status", "-socket", socketPath})
	if code != 1 {
		t.Fatalf("run(status) code = %d, want 1", code)
	}
	if !strings.Contains(errOut, "status query failed") {
		t.Fatalf("status stderr missing failure message: %q", errOut)
	}
}

func TestRunStatusSuccess(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "registry.sock")
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}
	defer ln.Close()

	done := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()

		var req statusRequest
		if err := readControlMessage(conn, &req); err != nil {
			done <- err
			return
		}
		if req.Cmd != "status" {
			done <- errors.New("unexpected command")
			return
		}

		resp := statusCommandResponse{
			Services: []statusService{
				{Name: "worker", ID: "worker-2", PID: 12347, Running: false},
				{Name: "api", ID: "api-1", PID: 12345, Running: true},
			},
		}
		done <- writeControlMessage(conn, resp)
	}()

	code, out, errOut := runWithCapturedOutput(t, []string{"status", "-socket", socketPath})
	if code != 0 {
		t.Fatalf("run(status) code = %d, want 0 (stderr=%q)", code, errOut)
	}
	if err := <-done; err != nil {
		t.Fatalf("status test server failed: %v", err)
	}

	if !strings.Contains(out, "SERVICE") {
		t.Fatalf("status output missing header: %q", out)
	}
	if !strings.Contains(out, "api-1") || !strings.Contains(out, "running") {
		t.Fatalf("status output missing running row: %q", out)
	}
	if !strings.Contains(out, "worker-2") || !strings.Contains(out, "stopped") {
		t.Fatalf("status output missing stopped row: %q", out)
	}
}

func TestRunStatusReadTimeout(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "registry.sock")
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}
	defer ln.Close()

	prevDialTimeout := statusDialTimeout
	prevWriteTimeout := statusWriteTimeout
	prevReadTimeout := statusReadTimeout
	statusDialTimeout = 100 * time.Millisecond
	statusWriteTimeout = 50 * time.Millisecond
	statusReadTimeout = 30 * time.Millisecond
	t.Cleanup(func() {
		statusDialTimeout = prevDialTimeout
		statusWriteTimeout = prevWriteTimeout
		statusReadTimeout = prevReadTimeout
	})

	done := make(chan error, 1)
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			done <- acceptErr
			return
		}
		defer conn.Close()
		var req statusRequest
		if err := readControlMessage(conn, &req); err != nil {
			done <- err
			return
		}
		time.Sleep(200 * time.Millisecond)
		done <- nil
	}()

	start := time.Now()
	code, _, errOut := runWithCapturedOutput(t, []string{"status", "-socket", socketPath})
	elapsed := time.Since(start)
	if code != 1 {
		t.Fatalf("run(status) code = %d, want 1", code)
	}
	if !strings.Contains(errOut, "status query failed") {
		t.Fatalf("status stderr missing failure message: %q", errOut)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("expected status to fail fast due to read timeout, elapsed=%s", elapsed)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("status test server failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for status test server goroutine")
	}
}

func runWithCapturedOutput(t *testing.T, args []string) (int, string, string) {
	t.Helper()

	var outBuf bytes.Buffer
	var errBuf bytes.Buffer

	origOut := stdout
	origErr := stderr
	stdout = &outBuf
	stderr = &errBuf
	defer func() {
		stdout = origOut
		stderr = origErr
	}()

	code := run(args)
	return code, outBuf.String(), errBuf.String()
}

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "nexus.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return path
}
