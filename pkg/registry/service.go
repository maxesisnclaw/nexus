package registry

import "time"

// EndpointType is the protocol for a service endpoint.
type EndpointType string

const (
	// EndpointUDS indicates a Unix domain socket endpoint.
	EndpointUDS EndpointType = "uds"
	// EndpointTCP indicates a TCP endpoint.
	EndpointTCP EndpointType = "tcp"
)

// Endpoint describes a single reachable service address.
type Endpoint struct {
	// Type is the endpoint protocol.
	Type EndpointType `msgpack:"type"`
	// Addr is the endpoint address string.
	Addr string `msgpack:"addr"`
}

// ServiceInstance is a registered service instance.
type ServiceInstance struct {
	// Name is the logical service name.
	Name string `msgpack:"name"`
	// ID is the unique instance identifier.
	ID string `msgpack:"id"`
	// Node is the source daemon node id.
	Node string `msgpack:"node,omitempty"`
	// Capabilities lists advertised capability tags.
	Capabilities []string `msgpack:"capabilities,omitempty"`
	// Endpoints lists reachable network endpoints for this instance.
	Endpoints []Endpoint `msgpack:"endpoints"`
	// Metadata carries optional user-defined key/value data.
	Metadata map[string]string `msgpack:"metadata,omitempty"`
	// TTL defines heartbeat expiry duration.
	TTL time.Duration `msgpack:"ttl"`
	// UpdatedAt is the last heartbeat or registration timestamp in UTC.
	UpdatedAt time.Time `msgpack:"updated_at"`
}

// ChangeType is the service lifecycle event kind.
type ChangeType string

const (
	// ChangeUp signals an instance registration/update event.
	ChangeUp ChangeType = "up"
	// ChangeDown signals an instance removal/expiry event.
	ChangeDown ChangeType = "down"
)

// ChangeEvent describes a service change notification.
type ChangeEvent struct {
	// Type is the lifecycle change type.
	Type ChangeType
	// Instance is the affected service instance snapshot.
	Instance ServiceInstance
}
