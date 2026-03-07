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
	seconds := int(math.Ceil(grace.Seconds()))
	if seconds <= 0 {
		seconds = 10
	}
	ctx, cancel := context.WithTimeout(context.Background(), grace+5*time.Second)
	defer cancel()

	stopArgs := []string{"stop", "--time", strconv.Itoa(seconds), container}
	stopCmd := execCommandContext(ctx, "docker", stopArgs...)
	stopOut, stopErr := stopCmd.CombinedOutput()

	// Best-effort remove regardless of stop result.
	rmCmd := execCommandContext(ctx, "docker", "rm", "-f", container)
	_, _ = rmCmd.CombinedOutput()

	// Check if container is actually gone.
	running, checkErr := d.IsRunning(container)
	if checkErr == nil && running {
		return fmt.Errorf("docker stop %s: container still running after stop and rm -f", container)
	}
	if stopErr != nil && checkErr != nil {
		return fmt.Errorf("docker stop %s: stop failed and cannot verify state: %w", container, errors.Join(stopErr, checkErr))
	}

	if stopErr != nil {
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
		return false, fmt.Errorf("docker inspect failed: %w", err)
	}
	return strings.TrimSpace(string(out)) == "true", nil
}

func dockerContainerName(proc *ManagedProcess) string {
	cleanID := dockerContainerNameSanitizer.ReplaceAllString(proc.ID, "-")
	return "nexus-" + cleanID
}
