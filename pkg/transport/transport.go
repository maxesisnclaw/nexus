package transport

import (
	"context"
	"errors"
	"fmt"
)

var (
	// ErrFDUnsupported indicates fd transfer is not available for this connection.
	ErrFDUnsupported = errors.New("fd transfer unsupported")
)

// Message is the shared request/response envelope.
type Message struct {
	Method  string            `msgpack:"method"`
	Payload []byte            `msgpack:"payload"`
	Headers map[string]string `msgpack:"headers,omitempty"`
}

// ServiceEndpoint describes where and how a service can be reached.
type ServiceEndpoint struct {
	Name    string
	Local   bool
	UDSAddr string
	TCPAddr string
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

// Dial picks the best transport for the endpoint.
func (r *Router) Dial(ctx context.Context, target ServiceEndpoint) (Conn, error) {
	if target.Local && target.UDSAddr != "" {
		return r.uds.Dial(ctx, target)
	}
	if target.TCPAddr != "" {
		return r.tcp.Dial(ctx, target)
	}
	if target.UDSAddr != "" {
		return r.uds.Dial(ctx, target)
	}
	return nil, fmt.Errorf("endpoint %s has no usable address", target.Name)
}

// Listen delegates to UDS by default for daemon-local listeners.
func (r *Router) Listen(ctx context.Context, addr string) (Listener, error) {
	return r.uds.Listen(ctx, addr)
}
