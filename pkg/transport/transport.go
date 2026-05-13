package transport

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

var (
	// ErrFDUnsupported indicates fd transfer is not available for this connection.
	ErrFDUnsupported = errors.New("fd transfer unsupported")
)

// Message is the shared request/response envelope.
type Message struct {
	// Method is the RPC method name.
	Method string `msgpack:"method"`
	// Payload is the raw request or response body.
	Payload []byte `msgpack:"payload"`
	// Headers carries optional metadata for the message.
	Headers map[string]string `msgpack:"headers,omitempty"`
}

// ServiceEndpoint describes where and how a service can be reached.
type ServiceEndpoint struct {
	// Name is the target service name.
	Name string
	// Local indicates whether the target runs on the same machine.
	Local bool
	// UDSAddr is the Unix domain socket address.
	UDSAddr string
	// TCPAddr is the TCP address.
	TCPAddr string
	// PublicKey is the Noise static public key for TCP encryption.
	PublicKey []byte
}

// Transport is the common abstraction over different network transports.
type Transport interface {
	Dial(ctx context.Context, target ServiceEndpoint) (Conn, error)
	Listen(ctx context.Context, addr string) (Listener, error)
}

// Listener accepts incoming transport connections.
type Listener interface {
	Accept(ctx context.Context) (Conn, error)
	Close() error
	Addr() string
}

// Conn sends and receives msgpack messages.
type Conn interface {
	Send(msg *Message) error
	Recv() (*Message, error)
	SendFd(fd int, metadata []byte) error
	RecvFd() (fd int, metadata []byte, err error)
	SetReadDeadline(t time.Time) error
	SetWriteDeadline(t time.Time) error
	Close() error
}

// Router chooses UDS or TCP based on endpoint locality.
type Router struct {
	uds Transport
	tcp Transport
}

// NewRouter creates an auto-routing transport facade.
func NewRouter(uds, tcp Transport) *Router {
	return &Router{uds: uds, tcp: tcp}
}

// IsUDSReachable reports whether the endpoint's UDS path is usable from this
// process. ep.Local == true is treated as a caller-asserted fast path
// (used by daemon-local lookups). When Local is false we stat the path: in
// container deployments that share a UDS via a named volume / bind mount,
// caller and callee have different hostnames but the same UDS path, and the
// socket file itself is the source of truth for reachability.
func IsUDSReachable(ep ServiceEndpoint) bool {
	if ep.UDSAddr == "" {
		return false
	}
	if ep.Local {
		return true
	}
	info, err := os.Stat(ep.UDSAddr)
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeSocket != 0
}

// Dial picks the best transport for the endpoint.
//
// Selection order:
//  1. UDS, if the socket file is reachable in this namespace (see
//     IsUDSReachable). This correctly handles container deployments that
//     share a UDS via a bind mount / named volume.
//  2. TCP, if TCPAddr is set.
//  3. Explanatory error.
func (r *Router) Dial(ctx context.Context, target ServiceEndpoint) (Conn, error) {
	if IsUDSReachable(target) {
		if r.uds == nil {
			return nil, errors.New("uds transport is not configured")
		}
		return r.uds.Dial(ctx, target)
	}
	if target.TCPAddr != "" {
		if r.tcp == nil {
			return nil, errors.New("tcp transport is not configured")
		}
		return r.tcp.Dial(ctx, target)
	}
	if target.UDSAddr != "" {
		return nil, fmt.Errorf(
			"endpoint %s: UDS %s not reachable in this namespace and no TCP fallback",
			target.Name, target.UDSAddr)
	}
	return nil, fmt.Errorf("endpoint %s has no usable address", target.Name)
}

// Listen delegates to UDS by default for daemon-local listeners.
func (r *Router) Listen(ctx context.Context, addr string) (Listener, error) {
	if r.uds == nil {
		return nil, errors.New("uds transport is not configured")
	}
	return r.uds.Listen(ctx, addr)
}
