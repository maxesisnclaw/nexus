package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseWithDefaults(t *testing.T) {
	data := []byte(`
[daemon]
health_interval = "3s"

[[service]]
name = "svc"
type = "singleton"
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
