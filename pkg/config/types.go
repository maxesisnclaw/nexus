package config

import "time"

// Config is the top-level Nexus daemon configuration.
type Config struct {
	// Daemon contains daemon-level runtime settings.
	Daemon DaemonConfig `toml:"daemon"`
	// Services lists all managed service definitions.
	Services []ServiceSpec `toml:"service"`
}

// DaemonConfig controls nexusd runtime behavior.
type DaemonConfig struct {
	// Socket is the daemon UDS socket path.
	Socket string `toml:"socket"`
	// LogLevel configures daemon logging verbosity.
	LogLevel string `toml:"log_level"`
	// HealthInterval is the liveness check interval.
	HealthInterval Duration `toml:"health_interval"`
	// ShutdownGrace is the graceful stop timeout before force kill.
	ShutdownGrace Duration `toml:"shutdown_grace"`
	// Peers lists remote daemon peers for registry sync.
	Peers []PeerConfig `toml:"peers"`
	// Listen is the optional TCP listen address for cross-node communication.
	Listen string `toml:"listen"`
}

// PeerConfig describes another daemon node for registry sync.
type PeerConfig struct {
	// Addr is the peer daemon address.
	Addr string `toml:"addr"`
}

// ServiceSpec describes one managed service entry.
type ServiceSpec struct {
	// Name is the service name.
	Name string `toml:"name"`
	// Type is the service deployment type such as singleton or worker.
	Type string `toml:"type"`
	// Runtime selects how the service is launched, for example binary or docker.
	Runtime string `toml:"runtime"`
	// Binary is the executable path for binary runtime.
	Binary string `toml:"binary"`
	// Image is the container image for docker runtime.
	Image string `toml:"image"`
	// Args are default startup arguments.
	Args []string `toml:"args"`
	// Volumes lists docker bind mounts.
	Volumes []string `toml:"volumes"`
	// DependsOn lists service names this service depends on. Reserved for future use; currently not enforced by the daemon.
	DependsOn []string `toml:"depends_on"`
	// HealthCheck endpoint for liveness probing. Reserved for future use; currently not enforced by the daemon.
	HealthCheck string `toml:"health_check"`
	// Network controls service transport exposure.
	Network string `toml:"network"`
	// Instances contains per-instance overrides for worker services.
	Instances []InstanceSpec `toml:"instances"`
}

// InstanceSpec is a worker instance override.
type InstanceSpec struct {
	// ID is the worker instance identifier.
	ID string `toml:"id"`
	// Args overrides service-level args for this instance.
	Args []string `toml:"args"`
}

// Duration is a TOML-parsed duration helper.
type Duration struct {
	// Duration is the parsed time duration value.
	time.Duration
}

// UnmarshalText parses duration strings from TOML.
func (d *Duration) UnmarshalText(text []byte) error {
	if len(text) == 0 {
		d.Duration = 0
		return nil
	}
	v, err := time.ParseDuration(string(text))
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

// MarshalText serializes duration as string.
func (d Duration) MarshalText() ([]byte, error) {
	return []byte(d.Duration.String()), nil
}
