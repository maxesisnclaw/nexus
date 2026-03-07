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
	// Socket is reserved for daemon UDS control-plane socket wiring.
	// Current runtime uses this field for the daemon control socket.
	Socket string `toml:"socket"`
	// LogLevel configures daemon logging verbosity.
	LogLevel string `toml:"log_level"`
	// HealthInterval is the liveness check interval.
	HealthInterval Duration `toml:"health_interval"`
	// ShutdownGrace is the graceful stop timeout before force kill.
	ShutdownGrace Duration `toml:"shutdown_grace"`
	// Peer-based registry sync is planned for a future version.
	// Listen is reserved for future daemon cross-node TCP listener wiring.
	// Current runtime does not consume this field yet.
	Listen string `toml:"listen"`
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
	// WorkDir is the working directory for binary runtime.
	WorkDir string `toml:"work_dir"`
	// Image is the container image for docker runtime.
	Image string `toml:"image"`
	// Args are default startup arguments.
	Args []string `toml:"args"`
	// Env contains environment variables for binary and docker runtimes.
	Env map[string]string `toml:"env"`
	// Volumes lists docker bind mounts.
	Volumes []string `toml:"volumes"`
	// Ports lists docker port mappings like host:container or host:container/proto.
	Ports []string `toml:"ports"`
	// CapAdd lists Linux capabilities to add for docker runtime.
	CapAdd []string `toml:"cap_add"`
	// CapDrop lists Linux capabilities to drop for docker runtime.
	CapDrop []string `toml:"cap_drop"`
	// DockerNetwork sets docker --network mode.
	DockerNetwork string `toml:"docker_network"`
	// ExtraArgs appends raw docker run flags.
	ExtraArgs []string `toml:"extra_args"`
	// DependsOn lists service names this service depends on.
	DependsOn []string `toml:"depends_on"`
	// HealthCheck endpoint for probe-based liveness checking.
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
