package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

var dockerPortMappingPattern = regexp.MustCompile(`^([0-9]{1,5}):([0-9]{1,5})(/(tcp|udp))?$`)

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
		if cfg.Services[i].Type == "" {
			cfg.Services[i].Type = "singleton"
		}
		if cfg.Services[i].Runtime == "" {
			cfg.Services[i].Runtime = "binary"
		}
		if cfg.Services[i].Network == "" {
			cfg.Services[i].Network = "uds"
		}
		for j := range cfg.Services[i].CapAdd {
			cfg.Services[i].CapAdd[j] = strings.ToUpper(cfg.Services[i].CapAdd[j])
		}
		for j := range cfg.Services[i].CapDrop {
			cfg.Services[i].CapDrop[j] = strings.ToUpper(cfg.Services[i].CapDrop[j])
		}
	}
}

func validate(cfg *Config) error {
	if cfg.Daemon.HealthInterval.Duration <= 0 {
		return errors.New("daemon.health_interval must be > 0")
	}
	seen := make(map[string]struct{}, len(cfg.Services))
	instanceOwner := make(map[string]string, len(cfg.Services))
	for _, svc := range cfg.Services {
		if svc.Name == "" {
			return errors.New("service.name is required")
		}
		if _, ok := seen[svc.Name]; ok {
			return fmt.Errorf("duplicate service name %q", svc.Name)
		}
		seen[svc.Name] = struct{}{}
		switch svc.Type {
		case "singleton":
			if len(svc.Instances) > 0 {
				return fmt.Errorf("singleton service %s must not define instances", svc.Name)
			}
		case "worker":
			if len(svc.Instances) == 0 {
				return fmt.Errorf("worker service %s requires at least one instance", svc.Name)
			}
		default:
			return fmt.Errorf("service %s type must be singleton or worker, got %q", svc.Name, svc.Type)
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
		if svc.WorkDir != "" && !filepath.IsAbs(svc.WorkDir) {
			return fmt.Errorf("service %s work_dir must be an absolute path, got %q", svc.Name, svc.WorkDir)
		}
		if svc.Runtime == "docker" && svc.Image == "" {
			return fmt.Errorf("service %s image is required", svc.Name)
		}
		if svc.Runtime == "docker" {
			for _, port := range svc.Ports {
				if !isValidDockerPortMapping(port) {
					return fmt.Errorf("service %s ports entry %q must match hostPort:containerPort or hostPort:containerPort/proto", svc.Name, port)
				}
			}
		}
		validNetworks := map[string]bool{"uds": true, "tcp": true, "dual": true}
		if !validNetworks[svc.Network] {
			return fmt.Errorf("service %s network must be uds, tcp, or dual, got %q", svc.Name, svc.Network)
		}
		for _, id := range expandedInstanceIDs(svc) {
			if owner, exists := instanceOwner[id]; exists {
				return fmt.Errorf(
					"duplicate expanded instance ID %q for services %q and %q",
					id,
					owner,
					svc.Name,
				)
			}
			instanceOwner[id] = svc.Name
		}
	}
	return nil
}

func expandedInstanceIDs(svc ServiceSpec) []string {
	if svc.Type == "worker" && len(svc.Instances) > 0 {
		ids := make([]string, 0, len(svc.Instances))
		for i, inst := range svc.Instances {
			id := inst.ID
			if id == "" {
				id = fmt.Sprintf("%s-%d", svc.Name, i)
			}
			ids = append(ids, id)
		}
		return ids
	}
	return []string{svc.Name}
}

func isValidDockerPortMapping(value string) bool {
	matches := dockerPortMappingPattern.FindStringSubmatch(value)
	if len(matches) == 0 {
		return false
	}
	host, err := strconv.Atoi(matches[1])
	if err != nil || host < 1 || host > 65535 {
		return false
	}
	container, err := strconv.Atoi(matches[2])
	if err != nil || container < 1 || container > 65535 {
		return false
	}
	return true
}
