package daemon

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"regexp"
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
	args := []string{"run", "-d", "--name", name}
	for _, v := range proc.Spec.Volumes {
		args = append(args, "-v", v)
	}
	if proc.Spec.Network == "uds" || proc.Spec.Network == "dual" {
		args = append(args, "-v", "/run/nexus:/run/nexus")
	}
	args = append(args, proc.Spec.Image)
	args = append(args, proc.Args...)

	cmd := execCommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker run failed: %w (%s)", err, bytes.TrimSpace(out))
	}
	id := strings.TrimSpace(string(out))
	d.logger.Info("docker container started", "service", proc.Service, "id", proc.ID, "container", name, "container_id", id)
	return name, nil
}

func (d *dockerCLI) Stop(container string, grace time.Duration) error {
	seconds := int(grace.Seconds())
	if seconds <= 0 {
		seconds = 10
	}
	ctx, cancel := context.WithTimeout(context.Background(), grace+5*time.Second)
	defer cancel()

	stopArgs := []string{"stop", "--time", strconv.Itoa(seconds), container}
	stopCmd := execCommandContext(ctx, "docker", stopArgs...)
	if out, err := stopCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker stop failed: %w (%s)", err, bytes.TrimSpace(out))
	}

	// Cleanup is best-effort because stopped containers might already be auto-removed.
	rmCmd := execCommandContext(ctx, "docker", "rm", "-f", container)
	_, _ = rmCmd.CombinedOutput()
	d.logger.Info("docker container stopped", "container", container)
	return nil
}

func (d *dockerCLI) IsRunning(container string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := execCommandContext(ctx, "docker", "inspect", "-f", "{{.State.Running}}", container)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("docker inspect failed: %w", err)
	}
	return strings.TrimSpace(string(out)) == "true", nil
}

func dockerContainerName(proc *ManagedProcess) string {
	cleanID := dockerContainerNameSanitizer.ReplaceAllString(proc.ID, "-")
	return "nexus-" + cleanID
}
