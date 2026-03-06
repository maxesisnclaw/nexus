package sdk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"syscall"
	"time"

	"nexus/pkg/registry"
	"nexus/pkg/transport"
)

const (
	fdCallMethod = "__nexus_fd_call__"
)

// Config controls SDK client behavior.
type Config struct {
	Name                  string
	ID                    string
	Capabilities          []string
	UDSAddr               string
	TCPAddr               string
	Network               string
	RequestTimeout        time.Duration
	LargePayloadThreshold int
	CallRetries           int
	RetryBackoff          time.Duration
	Registry              *registry.Registry
	Router                *transport.Router
	Logger                *slog.Logger
}

// Request represents an incoming method invocation.
type Request struct {
	Method  string
	Payload []byte
	Headers map[string]string
}

// Response represents handler output.
type Response struct {
	Payload []byte
	Headers map[string]string
}

// Handler handles one rpc invocation.
type Handler func(*Request) (*Response, error)

// Client provides service registration, serving, and rpc invocation.
type Client struct {
	cfg       Config
	logger    *slog.Logger
	registry  *registry.Registry
	discovery *registry.Discovery
	router    *transport.Router

	handlers map[string]Handler

	mu         sync.RWMutex
	listener   transport.Listener
	heartbeat  chan struct{}
	registered bool
}

// New creates a new SDK client instance.
func New(cfg Config) (*Client, error) {
	if cfg.Name == "" {
		return nil, errors.New("sdk name is required")
	}
	if cfg.ID == "" {
		cfg.ID = cfg.Name
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 5 * time.Second
	}
	if cfg.LargePayloadThreshold <= 0 {
		cfg.LargePayloadThreshold = 1 << 20
	}
	if cfg.RetryBackoff <= 0 {
		cfg.RetryBackoff = 100 * time.Millisecond
	}
	if cfg.CallRetries < 0 {
		cfg.CallRetries = 0
	}
	if cfg.Network == "" {
		cfg.Network = "uds"
	}
	if cfg.Registry == nil {
		cfg.Registry = registry.New("local")
	}
	if cfg.Router == nil {
		cfg.Router = transport.NewRouter(transport.NewUDSTransport(), transport.NewTCPTransport())
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}

	return &Client{
		cfg:       cfg,
		logger:    logger,
		registry:  cfg.Registry,
		discovery: registry.NewDiscovery(cfg.Registry),
		router:    cfg.Router,
		handlers:  make(map[string]Handler),
		heartbeat: make(chan struct{}),
	}, nil
}

// Handle registers a handler for one method.
func (c *Client) Handle(method string, handler Handler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.handlers[method] = handler
}

// Call invokes a method on one service instance picked by discovery.
func (c *Client) Call(serviceName, method string, payload []byte) (*Response, error) {
	return c.callWithRetry(serviceName, method, payload, false)
}

// CallWithData attempts fd-based transfer on local UDS and falls back to Call.
func (c *Client) CallWithData(serviceName, method string, payload []byte) (*Response, error) {
	if len(payload) < c.cfg.LargePayloadThreshold {
		return c.callWithRetry(serviceName, method, payload, false)
	}
	return c.callWithRetry(serviceName, method, payload, true)
}

func (c *Client) callWithRetry(serviceName, method string, payload []byte, preferFD bool) (*Response, error) {
	var lastErr error
	for attempt := 0; attempt <= c.cfg.CallRetries; attempt++ {
		resp, err := c.callOnce(serviceName, method, payload, preferFD)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if attempt == c.cfg.CallRetries {
			break
		}
		time.Sleep(c.cfg.RetryBackoff)
	}
	return nil, lastErr
}

func (c *Client) callOnce(serviceName, method string, payload []byte, preferFD bool) (*Response, error) {
	inst, err := c.discovery.Pick(serviceName)
	if err != nil {
		return nil, err
	}
	endpoint, err := endpointFromInstance(inst)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.RequestTimeout)
	defer cancel()
	conn, err := c.router.Dial(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if !preferFD {
		return c.callMsgpack(conn, method, payload)
	}

	fd, err := transport.CreateMemfd("nexus-call", payload)
	if err != nil {
		return c.callMsgpack(conn, method, payload)
	}
	defer syscall.Close(fd)

	setup := &transport.Message{Method: fdCallMethod, Headers: map[string]string{"method": method}}
	if err := conn.Send(setup); err != nil {
		return c.callOnce(serviceName, method, payload, false)
	}
	if err := conn.SendFd(fd, []byte("fd")); err != nil {
		return c.callOnce(serviceName, method, payload, false)
	}
	resp, err := conn.Recv()
	if err != nil {
		return nil, err
	}
	if msgErr, ok := resp.Headers["error"]; ok {
		return nil, errors.New(msgErr)
	}
	return &Response{Payload: resp.Payload, Headers: resp.Headers}, nil
}

func (c *Client) callMsgpack(conn transport.Conn, method string, payload []byte) (*Response, error) {
	if err := conn.Send(&transport.Message{Method: method, Payload: payload}); err != nil {
		return nil, err
	}
	resp, err := conn.Recv()
	if err != nil {
		return nil, err
	}
	if msgErr, ok := resp.Headers["error"]; ok {
		return nil, errors.New(msgErr)
	}
	return &Response{Payload: resp.Payload, Headers: resp.Headers}, nil
}

// Serve starts serving requests from configured endpoint.
func (c *Client) Serve(ctx context.Context) error {
	listener, endpoint, err := c.listen(ctx)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.listener = listener
	c.mu.Unlock()

	c.register(endpoint)
	go c.heartbeatLoop()
	go func() {
		<-ctx.Done()
		_ = c.Close()
	}()

	for {
		conn, err := listener.Accept(ctx)
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				return err
			}
		}
		go c.serveConn(conn)
	}
}

// Close unregisters this instance and closes server listener.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.registered {
		c.registry.Unregister(c.cfg.ID)
		c.registered = false
	}
	select {
	case <-c.heartbeat:
	default:
		close(c.heartbeat)
	}
	if c.listener != nil {
		err := c.listener.Close()
		c.listener = nil
		return err
	}
	return nil
}

func (c *Client) register(endpoint registry.Endpoint) {
	inst := registry.ServiceInstance{
		Name:         c.cfg.Name,
		ID:           c.cfg.ID,
		Capabilities: c.cfg.Capabilities,
		TTL:          15 * time.Second,
		Endpoints:    []registry.Endpoint{endpoint},
	}
	if c.cfg.Network == "dual" && c.cfg.TCPAddr != "" && c.cfg.UDSAddr != "" {
		inst.Endpoints = []registry.Endpoint{{Type: registry.EndpointUDS, Addr: c.cfg.UDSAddr}, {Type: registry.EndpointTCP, Addr: c.cfg.TCPAddr}}
	}
	c.registry.Register(inst)
	c.mu.Lock()
	c.registered = true
	c.mu.Unlock()
}

func (c *Client) heartbeatLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-c.heartbeat:
			return
		case <-ticker.C:
			_ = c.registry.Heartbeat(c.cfg.ID)
		}
	}
}

func (c *Client) listen(ctx context.Context) (transport.Listener, registry.Endpoint, error) {
	if c.cfg.UDSAddr != "" {
		ln, err := transport.NewUDSTransport().Listen(ctx, c.cfg.UDSAddr)
		if err != nil {
			return nil, registry.Endpoint{}, err
		}
		return ln, registry.Endpoint{Type: registry.EndpointUDS, Addr: c.cfg.UDSAddr}, nil
	}
	if c.cfg.TCPAddr != "" {
		ln, err := transport.NewTCPTransport().Listen(ctx, c.cfg.TCPAddr)
		if err != nil {
			return nil, registry.Endpoint{}, err
		}
		return ln, registry.Endpoint{Type: registry.EndpointTCP, Addr: ln.Addr()}, nil
	}
	return nil, registry.Endpoint{}, errors.New("either uds_addr or tcp_addr is required")
}

func (c *Client) serveConn(conn transport.Conn) {
	defer conn.Close()
	for {
		msg, err := conn.Recv()
		if err != nil {
			return
		}
		req := &Request{Method: msg.Method, Payload: msg.Payload, Headers: msg.Headers}
		if msg.Method == fdCallMethod {
			fd, _, err := conn.RecvFd()
			if err != nil {
				_ = conn.Send(&transport.Message{Headers: map[string]string{"error": err.Error()}})
				continue
			}
			data, err := transport.ReadFDAll(fd)
			_ = syscall.Close(fd)
			if err != nil {
				_ = conn.Send(&transport.Message{Headers: map[string]string{"error": err.Error()}})
				continue
			}
			if req.Headers == nil {
				req.Headers = map[string]string{}
			}
			req.Method = req.Headers["method"]
			req.Payload = data
		}

		resp, err := c.dispatch(req)
		if err != nil {
			_ = conn.Send(&transport.Message{Headers: map[string]string{"error": err.Error()}})
			continue
		}
		_ = conn.Send(&transport.Message{Method: req.Method, Payload: resp.Payload, Headers: resp.Headers})
	}
}

func (c *Client) dispatch(req *Request) (*Response, error) {
	c.mu.RLock()
	handler, ok := c.handlers[req.Method]
	c.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("handler not found: %s", req.Method)
	}
	return handler(req)
}

func endpointFromInstance(inst registry.ServiceInstance) (transport.ServiceEndpoint, error) {
	endpoint := transport.ServiceEndpoint{Name: inst.Name}
	for _, ep := range inst.Endpoints {
		switch ep.Type {
		case registry.EndpointUDS:
			endpoint.UDSAddr = ep.Addr
			endpoint.Local = true
		case registry.EndpointTCP:
			endpoint.TCPAddr = ep.Addr
		}
	}
	if endpoint.UDSAddr == "" && endpoint.TCPAddr == "" {
		return transport.ServiceEndpoint{}, errors.New("instance has no endpoints")
	}
	return endpoint, nil
}
