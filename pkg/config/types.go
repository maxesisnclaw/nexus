package config

import "time"

// Config is the top-level Nexus daemon configuration.
type Config struct {
	Daemon   DaemonConfig  `toml:"daemon"`
	Services []ServiceSpec `toml:"service"`
}

// DaemonConfig controls nexusd runtime behavior.
type DaemonConfig struct {
	Socket         string       `toml:"socket"`
	LogLevel       string       `toml:"log_level"`
	HealthInterval Duration     `toml:"health_interval"`
	ShutdownGrace  Duration     `toml:"shutdown_grace"`
	Peers          []PeerConfig `toml:"peers"`
	Listen         string       `toml:"listen"`
}

// PeerConfig describes another daemon node for registry sync.
type PeerConfig struct {
	Addr string `toml:"addr"`
}

// ServiceSpec describes one managed service entry.
type ServiceSpec struct {
	Name        string         `toml:"name"`
	Type        string         `toml:"type"`
	Runtime     string         `toml:"runtime"`
	Binary      string         `toml:"binary"`
	Image       string         `toml:"image"`
	Args        []string       `toml:"args"`
	Volumes     []string       `toml:"volumes"`
	DependsOn   []string       `toml:"depends_on"`
	HealthCheck string         `toml:"health_check"`
	Network     string         `toml:"network"`
	Instances   []InstanceSpec `toml:"instances"`
}

// InstanceSpec is a worker instance override.
type InstanceSpec struct {
	ID   string   `toml:"id"`
	Args []string `toml:"args"`
}

// Duration is a TOML-parsed duration helper.
type Duration struct {
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
