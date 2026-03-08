package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseWithDefaults(t *testing.T) {
	data := []byte(`
[daemon]
health_interval = "3s"

[[service]]
name = "svc"
binary = "/bin/echo"
args = ["ok"]
`)

	cfg, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if cfg.Daemon.Socket != "/run/nexus/registry.sock" {
		t.Fatalf("unexpected socket: %s", cfg.Daemon.Socket)
	}
	if cfg.Daemon.HealthInterval.Duration.String() != "3s" {
		t.Fatalf("unexpected interval: %s", cfg.Daemon.HealthInterval.Duration)
	}
	if got := cfg.Services[0].Runtime; got != "binary" {
		t.Fatalf("unexpected runtime: %s", got)
	}
	if got := cfg.Services[0].Type; got != "singleton" {
		t.Fatalf("unexpected type default: %s", got)
	}
}

func TestParseRejectsMissingBinary(t *testing.T) {
	data := []byte(`
[[service]]
name = "svc"
type = "singleton"
`)
	if _, err := Parse(data); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestParseRejectsRelativeBinary(t *testing.T) {
	data := []byte(`
[[service]]
name = "svc"
runtime = "binary"
binary = "echo"
`)
	if _, err := Parse(data); err == nil {
		t.Fatal("expected relative binary validation error")
	}
}

func TestDurationMarshalRoundTrip(t *testing.T) {
	var d Duration
	if err := d.UnmarshalText([]byte("150ms")); err != nil {
		t.Fatalf("UnmarshalText() error = %v", err)
	}
	if d.Duration != 150*time.Millisecond {
		t.Fatalf("unexpected duration: %s", d.Duration)
	}
	text, err := d.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText() error = %v", err)
	}
	if string(text) != "150ms" {
		t.Fatalf("unexpected marshaled duration: %q", string(text))
	}
}

func TestDurationUnmarshalRejectsInvalid(t *testing.T) {
	var d Duration
	if err := d.UnmarshalText([]byte("not-a-duration")); err == nil {
		t.Fatal("expected duration parse error")
	}
}

func TestLoadAndParseErrorPaths(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "missing.toml")); err == nil {
		t.Fatal("expected missing file error")
	}
	if _, err := Parse([]byte("daemon = [")); err == nil {
		t.Fatal("expected decode error")
	}
}

func TestLoadFromDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nexus.toml")
	data := []byte(`
[daemon]
log_level = "debug"

[[service]]
name = "svc"
runtime = "docker"
image = "busybox:latest"
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Daemon.LogLevel != "debug" {
		t.Fatalf("unexpected log level: %s", cfg.Daemon.LogLevel)
	}
	if got := cfg.Services[0].Runtime; got != "docker" {
		t.Fatalf("unexpected runtime: %s", got)
	}
}

func TestParseRejectsInvalidRuntimeAndMissingImage(t *testing.T) {
	invalidRuntime := []byte(`
[[service]]
name = "svc"
runtime = "nodejs"
binary = "/bin/echo"
`)
	if _, err := Parse(invalidRuntime); err == nil {
		t.Fatal("expected runtime validation error")
	}

	missingImage := []byte(`
[[service]]
name = "svc"
runtime = "docker"
`)
	if _, err := Parse(missingImage); err == nil {
		t.Fatal("expected image validation error")
	}
}

func TestParseBinaryWorkDirAndEnv(t *testing.T) {
	data := []byte(`
[[service]]
name = "svc"
runtime = "binary"
binary = "/bin/echo"
work_dir = "/tmp"
env = { FOO = "bar", HELLO = "world" }
`)
	cfg, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	svc := cfg.Services[0]
	if svc.WorkDir != "/tmp" {
		t.Fatalf("unexpected work_dir: %q", svc.WorkDir)
	}
	if svc.Env["FOO"] != "bar" || svc.Env["HELLO"] != "world" {
		t.Fatalf("unexpected env map: %#v", svc.Env)
	}
}

func TestParseDockerEnhancedFields(t *testing.T) {
	data := []byte(`
[[service]]
name = "legacy"
type = "worker"
runtime = "docker"
image = "repo/legacy:latest"
env = { b = "2", a = "1" }
ports = ["8080:80", "8443:443/tcp"]
cap_add = ["net_admin"]
cap_drop = ["all"]
docker_network = "bridge"
extra_args = ["--privileged=false", "--log-driver", "json-file"]
volumes = ["/host:/container"]
instances = [{ id = "legacy-1" }]
`)
	cfg, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	svc := cfg.Services[0]
	if got := svc.Env["a"]; got != "1" {
		t.Fatalf("unexpected env value for a: %q", got)
	}
	if got := svc.Ports[1]; got != "8443:443/tcp" {
		t.Fatalf("unexpected port mapping: %q", got)
	}
	if got := svc.CapAdd[0]; got != "NET_ADMIN" {
		t.Fatalf("expected cap_add normalization to uppercase, got %q", got)
	}
	if got := svc.CapDrop[0]; got != "ALL" {
		t.Fatalf("expected cap_drop normalization to uppercase, got %q", got)
	}
	if got := svc.DockerNetwork; got != "bridge" {
		t.Fatalf("unexpected docker_network: %q", got)
	}
	if got := svc.ExtraArgs[0]; got != "--privileged=false" {
		t.Fatalf("unexpected extra arg: %q", got)
	}
}

func TestParseRejectsRelativeWorkDir(t *testing.T) {
	data := []byte(`
[[service]]
name = "svc"
runtime = "binary"
binary = "/bin/echo"
work_dir = "relative/path"
`)
	if _, err := Parse(data); err == nil {
		t.Fatal("expected work_dir absolute path validation error")
	}
}

func TestParseServiceTypeInstanceRules(t *testing.T) {
	workerWithoutInstances := []byte(`
[[service]]
name = "worker"
type = "worker"
runtime = "binary"
binary = "/bin/echo"
`)
	if _, err := Parse(workerWithoutInstances); err == nil {
		t.Fatal("expected worker instance validation error")
	}

	singletonWithInstances := []byte(`
[[service]]
name = "singleton"
type = "singleton"
runtime = "binary"
binary = "/bin/echo"
instances = [{ id = "singleton-1" }]
`)
	if _, err := Parse(singletonWithInstances); err == nil {
		t.Fatal("expected singleton instances validation error")
	}
}

func TestParseRejectsInvalidDockerPortMapping(t *testing.T) {
	data := []byte(`
[[service]]
name = "legacy"
type = "worker"
runtime = "docker"
image = "repo/legacy:latest"
ports = ["bad-port"]
instances = [{ id = "legacy-1" }]
`)
	if _, err := Parse(data); err == nil {
		t.Fatal("expected invalid docker ports validation error")
	}
}

func TestParseRejectsDuplicateServiceNames(t *testing.T) {
	data := []byte(`
[[service]]
name = "dup"
runtime = "binary"
binary = "/bin/echo"

[[service]]
name = "dup"
runtime = "binary"
binary = "/bin/echo"
`)
	_, err := Parse(data)
	if err == nil {
		t.Fatal("expected duplicate service name validation error")
	}
	if !strings.Contains(err.Error(), `duplicate service name "dup"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}
