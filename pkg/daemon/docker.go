package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var execCommandContext = exec.CommandContext
var dockerContainerNameSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_.-]`)

type dockerRuntime interface {
	Start(ctx context.Context, proc *ManagedProcess) (string, error)
	Stop(container string, grace time.Duration) error
	IsRunning(container string) (bool, error)
}

type dockerCLI struct {
	logger *slog.Logger
}

func newDockerRuntime(logger *slog.Logger) dockerRuntime {
	return &dockerCLI{logger: logger}
}

func (d *dockerCLI) Start(ctx context.Context, proc *ManagedProcess) (string, error) {
	name := dockerContainerName(proc)
	args := dockerRunArgs(proc, name)

	cmd := execCommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker run failed: %w (%s)", err, bytes.TrimSpace(out))
	}
	id := strings.TrimSpace(string(out))
	d.logger.Info("docker container started", "service", proc.Service, "id", proc.ID, "container", name, "container_id", id)
	return name, nil
}

func dockerRunArgs(proc *ManagedProcess, name string) []string {
	args := []string{"run", "-d", "--name", name}
	if proc.Spec.DockerNetwork != "" {
		args = append(args, "--network", proc.Spec.DockerNetwork)
	}

	envKeys := make([]string, 0, len(proc.Spec.Env))
	for key := range proc.Spec.Env {
		envKeys = append(envKeys, key)
	}
	sort.Strings(envKeys)
	for _, key := range envKeys {
		args = append(args, "-e", fmt.Sprintf("%s=%s", key, proc.Spec.Env[key]))
	}

	for _, volume := range proc.Spec.Volumes {
		args = append(args, "-v", volume)
	}
	for _, port := range proc.Spec.Ports {
		args = append(args, "-p", port)
	}
	for _, cap := range proc.Spec.CapAdd {
		args = append(args, "--cap-add", cap)
	}
	for _, cap := range proc.Spec.CapDrop {
		args = append(args, "--cap-drop", cap)
	}

	if proc.Spec.Network == "uds" || proc.Spec.Network == "dual" {
		args = append(args, "-v", "/run/nexus:/run/nexus")
	}
	args = append(args, proc.Spec.ExtraArgs...)
	args = append(args, proc.Spec.Image)
	args = append(args, proc.Args...)
	return args
}

func (d *dockerCLI) Stop(container string, grace time.Duration) error {
	seconds := int(math.Ceil(grace.Seconds()))
	if seconds <= 0 {
		seconds = 10
	}
	ctx, cancel := context.WithTimeout(context.Background(), grace+5*time.Second)
	defer cancel()

	stopArgs := []string{"stop", "--time", strconv.Itoa(seconds), container}
	stopCmd := execCommandContext(ctx, "docker", stopArgs...)
	stopOut, stopErr := stopCmd.CombinedOutput()
	stopMissing := dockerContainerNotFound(stopOut)

	// Best-effort remove regardless of stop result.
	rmCmd := execCommandContext(ctx, "docker", "rm", "-f", container)
	_, _ = rmCmd.CombinedOutput()

	// Check if container is actually gone.
	running, checkErr := d.IsRunning(container)
	if checkErr == nil && running {
		return fmt.Errorf("docker stop %s: container still running after stop and rm -f", container)
	}
	if stopErr != nil && !stopMissing && checkErr != nil {
		return fmt.Errorf("docker stop %s: stop failed and cannot verify state: %w", container, errors.Join(stopErr, checkErr))
	}

	if stopErr != nil && stopMissing {
		d.logger.Info("docker container already absent", "container", container)
	} else if stopErr != nil {
		d.logger.Info("docker container stopped (was already exited)", "container", container)
	} else {
		d.logger.Info("docker container stopped", "container", container)
	}
	_ = stopOut
	return nil
}

func (d *dockerCLI) IsRunning(container string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := execCommandContext(ctx, "docker", "inspect", "-f", "{{.State.Running}}", container)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if dockerContainerNotFound(out) {
			return false, nil
		}
		return false, fmt.Errorf("docker inspect failed: %w (%s)", err, bytes.TrimSpace(out))
	}
	return strings.TrimSpace(string(out)) == "true", nil
}

func dockerContainerNotFound(out []byte) bool {
	text := strings.ToLower(string(out))
	return strings.Contains(text, "no such container") || strings.Contains(text, "no such object")
}

func dockerContainerName(proc *ManagedProcess) string {
	cleanID := dockerContainerNameSanitizer.ReplaceAllString(proc.ID, "-")
	return "nexus-" + cleanID
}
