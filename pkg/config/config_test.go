package config

import "testing"

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
