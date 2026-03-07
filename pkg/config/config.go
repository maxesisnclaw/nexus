package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
)

const (
	defaultSocket         = "/run/nexus/registry.sock"
	defaultHealthInterval = 5 * time.Second
	defaultShutdownGrace  = 10 * time.Second
)

// Load reads a TOML file from disk and returns validated configuration.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return Parse(data)
}

// Parse decodes and validates TOML configuration bytes.
func Parse(data []byte) (*Config, error) {
	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("decode toml: %w", err)
	}
	applyDefaults(&cfg)
	if err := validate(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func applyDefaults(cfg *Config) {
	if cfg.Daemon.Socket == "" {
		cfg.Daemon.Socket = defaultSocket
	}
	if cfg.Daemon.LogLevel == "" {
		cfg.Daemon.LogLevel = "info"
	}
	if cfg.Daemon.HealthInterval.Duration <= 0 {
		cfg.Daemon.HealthInterval = Duration{Duration: defaultHealthInterval}
	}
	if cfg.Daemon.ShutdownGrace.Duration <= 0 {
		cfg.Daemon.ShutdownGrace = Duration{Duration: defaultShutdownGrace}
	}
	for i := range cfg.Services {
		if cfg.Services[i].Runtime == "" {
			cfg.Services[i].Runtime = "binary"
		}
		if cfg.Services[i].Network == "" {
			cfg.Services[i].Network = "uds"
		}
	}
}

func validate(cfg *Config) error {
	if cfg.Daemon.HealthInterval.Duration <= 0 {
		return errors.New("daemon.health_interval must be > 0")
	}
	for _, svc := range cfg.Services {
		if svc.Name == "" {
			return errors.New("service.name is required")
		}
		if svc.Runtime != "binary" && svc.Runtime != "docker" {
			return fmt.Errorf("service %s runtime must be binary or docker", svc.Name)
		}
		if svc.Runtime == "binary" && svc.Binary == "" {
			return fmt.Errorf("service %s binary is required", svc.Name)
		}
		if svc.Runtime == "binary" && !filepath.IsAbs(svc.Binary) {
			return fmt.Errorf("service %s binary must be an absolute path, got %q", svc.Name, svc.Binary)
		}
		if svc.Runtime == "docker" && svc.Image == "" {
			return fmt.Errorf("service %s image is required", svc.Name)
		}
		validNetworks := map[string]bool{"uds": true, "tcp": true, "dual": true}
		if !validNetworks[svc.Network] {
			return fmt.Errorf("service %s network must be uds, tcp, or dual, got %q", svc.Name, svc.Network)
		}
	}
	return nil
}
