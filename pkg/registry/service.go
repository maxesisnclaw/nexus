package registry

import "time"

// EndpointType is the protocol for a service endpoint.
type EndpointType string

const (
	EndpointUDS EndpointType = "uds"
	EndpointTCP EndpointType = "tcp"
)

// Endpoint describes a single reachable service address.
type Endpoint struct {
	Type EndpointType `msgpack:"type"`
	Addr string       `msgpack:"addr"`
}

// ServiceInstance is a registered service instance.
type ServiceInstance struct {
	Name         string            `msgpack:"name"`
	ID           string            `msgpack:"id"`
	Node         string            `msgpack:"node,omitempty"`
	Capabilities []string          `msgpack:"capabilities,omitempty"`
	Endpoints    []Endpoint        `msgpack:"endpoints"`
	Metadata     map[string]string `msgpack:"metadata,omitempty"`
	TTL          time.Duration     `msgpack:"ttl"`
	UpdatedAt    time.Time         `msgpack:"updated_at"`
}

// ChangeType is the service lifecycle event kind.
type ChangeType string

const (
	ChangeUp   ChangeType = "up"
	ChangeDown ChangeType = "down"
)

// ChangeEvent describes a service change notification.
type ChangeEvent struct {
	Type     ChangeType
	Instance ServiceInstance
}
