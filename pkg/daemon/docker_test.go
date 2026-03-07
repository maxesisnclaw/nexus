package daemon

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maxesisn/nexus/pkg/config"
)

func TestDockerContainerName(t *testing.T) {
	name := dockerContainerName(&ManagedProcess{ID: "svc/a_b"})
	if name != "nexus-svc-a_b" {
		t.Fatalf("unexpected docker container name: %s", name)
	}
}

func TestDockerContainerNameSanitization(t *testing.T) {
	name := dockerContainerName(&ManagedProcess{ID: "svc/a b:c@d#e"})
	if name != "nexus-svc-a-b-c-d-e" {
		t.Fatalf("unexpected sanitized docker container name: %s", name)
	}
	if strings.HasPrefix(name, "-") || strings.HasPrefix(name, ".") {
		t.Fatalf("container name should not start with invalid character: %s", name)
	}
}

func TestDockerCLIStartStopAndInspect(t *testing.T) {
	recordPath := filepath.Join(t.TempDir(), "docker-args.log")
	restore := stubExecCommandContext(recordPath, "")
	defer restore()

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cli := &dockerCLI{logger: logger}
	proc := &ManagedProcess{
		ID:      "svc-1",
		Service: "svc",
		Spec:    configSpecForDockerTest(),
		Args:    []string{"--mode", "test"},
	}

	name, err := cli.Start(context.Background(), proc)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if name != "nexus-svc-1" {
		t.Fatalf("unexpected container name: %s", name)
	}
	running, err := cli.IsRunning(name)
	if err != nil {
		t.Fatalf("IsRunning() error = %v", err)
	}
	if !running {
		t.Fatal("expected running=true from helper")
	}
	if err := cli.Stop(name, 2*time.Second); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	data, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	logOutput := string(data)
	if !strings.Contains(logOutput, "run -d --name nexus-svc-1") {
		t.Fatalf("missing docker run invocation: %s", logOutput)
	}
	if !strings.Contains(logOutput, "-v /host:/container") {
		t.Fatalf("missing user volume flag: %s", logOutput)
	}
	if !strings.Contains(logOutput, "-v /run/nexus:/run/nexus") {
		t.Fatalf("missing nexus socket mount flag: %s", logOutput)
	}
	if !strings.Contains(logOutput, "inspect -f {{.State.Running}} nexus-svc-1") {
		t.Fatalf("missing inspect invocation: %s", logOutput)
	}
	if !strings.Contains(logOutput, "stop --time 2 nexus-svc-1") {
		t.Fatalf("missing stop invocation: %s", logOutput)
	}
	if !strings.Contains(logOutput, "rm -f nexus-svc-1") {
		t.Fatalf("missing cleanup invocation: %s", logOutput)
	}
}

func TestDockerCLIErrorPath(t *testing.T) {
	recordPath := filepath.Join(t.TempDir(), "docker-args.log")
	restore := stubExecCommandContext(recordPath, "run")
	defer restore()

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cli := &dockerCLI{logger: logger}
	_, err := cli.Start(context.Background(), &ManagedProcess{
		ID:   "svc-1",
		Spec: configSpecForDockerTest(),
	})
	if err == nil {
		t.Fatal("expected docker run failure")
	}
}

func stubExecCommandContext(recordPath, failSubcommand string) func() {
	orig := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		helperArgs := []string{"-test.run=TestDockerHelperProcess", "--", name}
		helperArgs = append(helperArgs, args...)
		cmd := exec.CommandContext(ctx, os.Args[0], helperArgs...)
		cmd.Env = append(os.Environ(),
			"GO_WANT_DOCKER_HELPER_PROCESS=1",
			fmt.Sprintf("NEXUS_DOCKER_RECORD=%s", recordPath),
			fmt.Sprintf("NEXUS_DOCKER_FAIL=%s", failSubcommand),
		)
		return cmd
	}
	return func() {
		execCommandContext = orig
	}
}

func TestDockerHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_DOCKER_HELPER_PROCESS") != "1" {
		return
	}
	args := os.Args
	idx := -1
	for i := range args {
		if args[i] == "--" {
			idx = i
			break
		}
	}
	if idx < 0 || idx+2 >= len(args) {
		os.Exit(2)
	}
	subArgs := args[idx+2:]
	recordPath := os.Getenv("NEXUS_DOCKER_RECORD")
	if recordPath != "" {
		f, err := os.OpenFile(recordPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			os.Exit(3)
		}
		_, _ = fmt.Fprintln(f, strings.Join(subArgs, " "))
		_ = f.Close()
	}
	fail := os.Getenv("NEXUS_DOCKER_FAIL")
	if len(subArgs) > 0 && subArgs[0] == fail {
		_, _ = fmt.Fprint(os.Stderr, "forced helper failure")
		os.Exit(7)
	}

	switch {
	case len(subArgs) > 0 && subArgs[0] == "run":
		_, _ = fmt.Fprintln(os.Stdout, "cid-test")
	case len(subArgs) > 0 && subArgs[0] == "inspect":
		_, _ = fmt.Fprintln(os.Stdout, "true")
	default:
	}
	os.Exit(0)
}

func configSpecForDockerTest() config.ServiceSpec {
	return config.ServiceSpec{
		Runtime: "docker",
		Image:   "repo/test:latest",
		Volumes: []string{"/host:/container"},
		Network: "dual",
	}
}
