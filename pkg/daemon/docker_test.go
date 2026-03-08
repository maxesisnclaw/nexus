package daemon

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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

func TestDockerCLIStopRoundsUpSubSecondGrace(t *testing.T) {
	recordPath := filepath.Join(t.TempDir(), "docker-args.log")
	restore := stubExecCommandContext(recordPath, "")
	defer restore()

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	cli := &dockerCLI{logger: logger}
	if err := cli.Stop("nexus-svc-1", 500*time.Millisecond); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	data, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !strings.Contains(string(data), "stop --time 1 nexus-svc-1") {
		t.Fatalf("expected stop timeout rounded up to 1 second, got %s", string(data))
	}
}

func TestDockerRunArgsOrderAndFields(t *testing.T) {
	proc := &ManagedProcess{
		ID:      "svc-1",
		Service: "svc",
		Spec: config.ServiceSpec{
			Runtime:       "docker",
			Image:         "repo/test:latest",
			Args:          []string{"--ignored-by-args-field"},
			Env:           map[string]string{"BETA": "2", "ALPHA": "1"},
			Volumes:       []string{"/host1:/container1", "/host2:/container2"},
			Ports:         []string{"8080:80", "8443:443/tcp"},
			CapAdd:        []string{"NET_ADMIN"},
			CapDrop:       []string{"ALL"},
			DockerNetwork: "bridge",
			ExtraArgs:     []string{"--restart", "always"},
			Network:       "dual",
		},
		Args: []string{"--mode", "test"},
	}

	got := dockerRunArgs(proc, "nexus-svc-1")
	want := []string{
		"run", "-d", "--name", "nexus-svc-1",
		"--network", "bridge",
		"-e", "ALPHA=1",
		"-e", "BETA=2",
		"-v", "/host1:/container1",
		"-v", "/host2:/container2",
		"-p", "8080:80",
		"-p", "8443:443/tcp",
		"--cap-add", "NET_ADMIN",
		"--cap-drop", "ALL",
		"-v", "/run/nexus:/run/nexus",
		"--restart", "always",
		"repo/test:latest",
		"--mode", "test",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dockerRunArgs() mismatch\nwant: %#v\ngot:  %#v", want, got)
	}
}

func TestBuildBinaryCommandUsesWorkDirAndMergedEnv(t *testing.T) {
	spec := config.ServiceSpec{
		Name:    "svc",
		Runtime: "binary",
		Binary:  "/bin/echo",
		WorkDir: "/tmp",
		Env: map[string]string{
			"NEXUS_PHASE1_TEST_VAR": "value-1",
			"PATH":                  "/tmp/nexus-path-override",
		},
	}
	cmd := buildBinaryCommand(spec, []string{"hello"})
	if cmd.Dir != "/tmp" {
		t.Fatalf("unexpected cmd.Dir: %q", cmd.Dir)
	}
	if got := lookupEnv(cmd.Env, "NEXUS_PHASE1_TEST_VAR"); got != "value-1" {
		t.Fatalf("unexpected merged env value: %q", got)
	}
	if got := lookupEnv(cmd.Env, "PATH"); got != "/tmp/nexus-path-override" {
		t.Fatalf("expected PATH override, got %q", got)
	}
}

func TestBuildBinaryCommandDefaultsWorkDirToBinaryDir(t *testing.T) {
	spec := config.ServiceSpec{
		Name:    "svc",
		Runtime: "binary",
		Binary:  "/usr/local/bin/example",
	}
	cmd := buildBinaryCommand(spec, []string{"hello"})
	if cmd.Dir != "/usr/local/bin" {
		t.Fatalf("unexpected default cmd.Dir: %q", cmd.Dir)
	}
}

func lookupEnv(env []string, key string) string {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}

func stubExecCommandContext(recordPath, failSubcommand string) func() {
	orig := execCommandContext
	statePath := recordPath + ".state"
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		helperArgs := []string{"-test.run=TestDockerHelperProcess", "--", name}
		helperArgs = append(helperArgs, args...)
		cmd := exec.CommandContext(ctx, os.Args[0], helperArgs...)
		cmd.Env = append(os.Environ(),
			"GO_WANT_DOCKER_HELPER_PROCESS=1",
			fmt.Sprintf("NEXUS_DOCKER_RECORD=%s", recordPath),
			fmt.Sprintf("NEXUS_DOCKER_FAIL=%s", failSubcommand),
			fmt.Sprintf("NEXUS_DOCKER_STATE=%s", statePath),
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
	statePath := os.Getenv("NEXUS_DOCKER_STATE")
	if len(subArgs) > 0 && subArgs[0] == fail {
		_, _ = fmt.Fprint(os.Stderr, "forced helper failure")
		os.Exit(7)
	}

	switch {
	case len(subArgs) > 0 && subArgs[0] == "run":
		if statePath != "" {
			_ = os.WriteFile(statePath, []byte("true"), 0o644)
		}
		_, _ = fmt.Fprintln(os.Stdout, "cid-test")
	case len(subArgs) > 0 && (subArgs[0] == "stop" || subArgs[0] == "rm"):
		if statePath != "" {
			_ = os.WriteFile(statePath, []byte("false"), 0o644)
		}
	case len(subArgs) > 0 && subArgs[0] == "inspect":
		running := "false"
		if statePath != "" {
			if data, err := os.ReadFile(statePath); err == nil && strings.TrimSpace(string(data)) == "true" {
				running = "true"
			}
		}
		_, _ = fmt.Fprintln(os.Stdout, running)
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
