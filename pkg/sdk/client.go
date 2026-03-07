package sdk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/maxesisn/nexus/pkg/registry"
	"github.com/maxesisn/nexus/pkg/transport"
)

const (
	fdCallMethod = "__nexus_fd_call__"
	fdReadyKey   = "ready_fd"
)

var createMemfd = transport.CreateMemfd

// Config controls SDK client behavior.
type Config struct {
	// Name is the service name to register.
	Name string
	// ID is the unique instance id; defaults to Name.
	ID string
	// Capabilities lists discovery capability tags.
	Capabilities []string
	// UDSAddr is the service UDS listen address.
	UDSAddr string
	// TCPAddr is the service TCP listen address.
	TCPAddr string
	// Network controls exposure mode such as uds, tcp, or dual.
	Network string
	// RequestTimeout bounds a single outbound call.
	RequestTimeout time.Duration
	// ServeTimeout bounds server-side request handling per connection cycle.
	ServeTimeout time.Duration
	// LargePayloadThreshold enables fd path for payloads at or above this size.
	LargePayloadThreshold int
	// CallRetries is the number of retry attempts for outbound calls.
	CallRetries int
	// RetryBackoff is the delay between retries.
	RetryBackoff time.Duration
	// Registry is the service registry backend.
	Registry *registry.Registry
	// Router is the transport router used for outbound dials.
	// Serve/listen paths use built-in UDS/TCP transports based on Network.
	Router *transport.Router
	// Logger receives SDK logs.
	Logger *slog.Logger
}

// Request represents an incoming method invocation.
type Request struct {
	// Method is the invoked method name.
	Method string
	// Payload is the request body.
	Payload []byte
	// Headers contains optional request metadata.
	Headers map[string]string
}

// Response represents handler output.
type Response struct {
	// Payload is the response body.
	Payload []byte
	// Headers contains optional response metadata.
	Headers map[string]string
}

// Handler handles one rpc invocation.
type Handler func(*Request) (*Response, error)

// Client provides service registration, serving, and rpc invocation.
type Client struct {
	cfg       Config
	logger    *slog.Logger
	registry  *registry.Registry
	ownsReg   bool
	discovery *registry.Discovery
	connPool  connectionPool

	handlers map[string]Handler

	localNodeID string

	mu         sync.RWMutex
	listener   transport.Listener
	heartbeat  chan struct{}
	registered bool
	closeOnce  sync.Once
	closeErr   error
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
	if cfg.ServeTimeout <= 0 {
		cfg.ServeTimeout = 30 * time.Second
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
	ownsReg := false
	if cfg.Registry == nil {
		cfg.Registry = registry.New("local")
		ownsReg = true
	}
	if cfg.Router == nil {
		cfg.Router = transport.NewRouter(transport.NewUDSTransport(), transport.NewTCPTransport())
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}

	return &Client{
		cfg:         cfg,
		logger:      logger,
		registry:    cfg.Registry,
		ownsReg:     ownsReg,
		discovery:   registry.NewDiscovery(cfg.Registry),
		connPool:    newConnectionPool(cfg.Router),
		handlers:    make(map[string]Handler),
		heartbeat:   make(chan struct{}),
		localNodeID: cfg.Registry.NodeID(),
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
	endpoint, err := endpointFromInstance(inst, c.localNodeID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.RequestTimeout)
	defer cancel()
	conn, err := c.connPool.Acquire(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	reusable := true
	defer func() {
		c.connPool.Release(endpoint, conn, reusable)
	}()

	if !preferFD {
		resp, connHealthy, err := c.callMsgpack(conn, method, payload)
		reusable = connHealthy
		return resp, err
	}

	fd, err := createMemfd("nexus-call", payload)
	if err != nil {
		resp, connHealthy, err := c.callMsgpack(conn, method, payload)
		reusable = connHealthy
		return resp, err
	}
	defer syscall.Close(fd)

	setup := &transport.Message{Method: fdCallMethod, Headers: map[string]string{"method": method}}
	if err := conn.Send(setup); err != nil {
		reusable = false
		return c.callMsgpackFallback(endpoint, method, payload)
	}
	ack, err := conn.Recv()
	if err != nil || ack.Headers[fdReadyKey] != "1" {
		reusable = false
		return c.callMsgpackFallback(endpoint, method, payload)
	}
	if err := conn.SendFd(fd, []byte("fd")); err != nil {
		reusable = false
		return c.callMsgpackFallback(endpoint, method, payload)
	}
	resp, err := conn.Recv()
	if err != nil {
		reusable = false
		return nil, err
	}
	if msgErr, ok := resp.Headers["error"]; ok {
		return nil, errors.New(msgErr)
	}
	return &Response{Payload: resp.Payload, Headers: resp.Headers}, nil
}

func (c *Client) callMsgpackFallback(endpoint transport.ServiceEndpoint, method string, payload []byte) (*Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.RequestTimeout)
	defer cancel()
	conn, err := c.connPool.Acquire(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	connHealthy := true
	defer func() {
		c.connPool.Release(endpoint, conn, connHealthy)
	}()
	resp, healthy, err := c.callMsgpack(conn, method, payload)
	connHealthy = healthy
	return resp, err
}

func (c *Client) callMsgpack(conn transport.Conn, method string, payload []byte) (*Response, bool, error) {
	if err := conn.Send(&transport.Message{Method: method, Payload: payload}); err != nil {
		return nil, false, err
	}
	resp, err := conn.Recv()
	if err != nil {
		return nil, false, err
	}
	if msgErr, ok := resp.Headers["error"]; ok {
		return nil, true, errors.New(msgErr)
	}
	return &Response{Payload: resp.Payload, Headers: resp.Headers}, true, nil
}

// Serve starts serving requests from configured endpoint.
func (c *Client) Serve(ctx context.Context) error {
	listeners, endpoints, err := c.listen(ctx)
	if err != nil {
		return err
	}
	listener := &listenerGroup{listeners: listeners}
	c.mu.Lock()
	c.listener = listener
	c.mu.Unlock()

	c.register(endpoints)
	go c.heartbeatLoop()
	go func() {
		<-ctx.Done()
		_ = c.Close()
	}()

	type acceptResult struct {
		conn transport.Conn
		err  error
	}
	acceptCh := make(chan acceptResult)
	for _, ln := range listeners {
		ln := ln
		go func() {
			for {
				conn, err := ln.Accept(ctx)
				if err != nil {
					select {
					case <-ctx.Done():
						return
					default:
					}
					select {
					case acceptCh <- acceptResult{err: err}:
					case <-ctx.Done():
					}
					return
				}
				select {
				case acceptCh <- acceptResult{conn: conn}:
				case <-ctx.Done():
					_ = conn.Close()
					return
				}
			}
		}()
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case result := <-acceptCh:
			if result.err != nil {
				return result.err
			}
			go c.serveConn(result.conn)
		}
	}
}

// Close unregisters this instance and closes server listener.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.registered {
			c.registry.Unregister(c.cfg.ID)
			c.registered = false
		}
		close(c.heartbeat)
		if c.listener != nil {
			c.closeErr = c.listener.Close()
			c.listener = nil
		}
		poolErr := c.connPool.Close()
		if c.closeErr != nil || poolErr != nil {
			c.closeErr = errors.Join(c.closeErr, poolErr)
		}
		if c.ownsReg {
			c.registry.Close()
		}
	})
	return c.closeErr
}

func (c *Client) register(endpoints []registry.Endpoint) {
	inst := registry.ServiceInstance{
		Name:         c.cfg.Name,
		ID:           c.cfg.ID,
		Capabilities: c.cfg.Capabilities,
		TTL:          15 * time.Second,
		Endpoints:    append([]registry.Endpoint(nil), endpoints...),
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

func (c *Client) listen(ctx context.Context) ([]transport.Listener, []registry.Endpoint, error) {
	mode := strings.ToLower(c.cfg.Network)
	if mode == "" {
		mode = "uds"
	}

	listeners := make([]transport.Listener, 0, 2)
	endpoints := make([]registry.Endpoint, 0, 2)
	closeListeners := func() {
		for _, ln := range listeners {
			_ = ln.Close()
		}
	}
	addListener := func(t transport.Transport, endpointType registry.EndpointType, addr string) error {
		ln, err := t.Listen(ctx, addr)
		if err != nil {
			return err
		}
		listeners = append(listeners, ln)
		endpoints = append(endpoints, registry.Endpoint{Type: endpointType, Addr: ln.Addr()})
		return nil
	}

	switch mode {
	case "dual":
		if c.cfg.UDSAddr == "" || c.cfg.TCPAddr == "" {
			return nil, nil, errors.New("dual network requires both uds_addr and tcp_addr")
		}
		if err := addListener(transport.NewUDSTransport(), registry.EndpointUDS, c.cfg.UDSAddr); err != nil {
			return nil, nil, err
		}
		if err := addListener(transport.NewTCPTransport(), registry.EndpointTCP, c.cfg.TCPAddr); err != nil {
			closeListeners()
			return nil, nil, err
		}
	case "tcp":
		if c.cfg.TCPAddr == "" {
			return nil, nil, errors.New("tcp network requires tcp_addr")
		}
		if err := addListener(transport.NewTCPTransport(), registry.EndpointTCP, c.cfg.TCPAddr); err != nil {
			return nil, nil, err
		}
	case "uds":
		if c.cfg.UDSAddr != "" {
			if err := addListener(transport.NewUDSTransport(), registry.EndpointUDS, c.cfg.UDSAddr); err != nil {
				return nil, nil, err
			}
			break
		}
		if c.cfg.TCPAddr != "" {
			if err := addListener(transport.NewTCPTransport(), registry.EndpointTCP, c.cfg.TCPAddr); err != nil {
				return nil, nil, err
			}
			break
		}
		return nil, nil, errors.New("either uds_addr or tcp_addr is required")
	default:
		return nil, nil, fmt.Errorf("unsupported network mode: %s", c.cfg.Network)
	}

	return listeners, endpoints, nil
}

func (c *Client) serveConn(conn transport.Conn) {
	defer conn.Close()
	for {
		msg, err := c.recvWithTimeout(conn)
		if err != nil {
			return
		}
		req := &Request{Method: msg.Method, Payload: msg.Payload, Headers: msg.Headers}
		if msg.Method == fdCallMethod {
			if err := conn.Send(&transport.Message{Method: fdCallMethod, Headers: map[string]string{fdReadyKey: "1"}}); err != nil {
				return
			}
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

func (c *Client) recvWithTimeout(conn transport.Conn) (*transport.Message, error) {
	if c.cfg.ServeTimeout <= 0 {
		return conn.Recv()
	}

	deadline := time.Now().Add(c.cfg.ServeTimeout)
	if err := conn.SetReadDeadline(deadline); err != nil {
		return nil, err
	}
	defer func() {
		_ = conn.SetReadDeadline(time.Time{})
	}()

	msg, err := conn.Recv()
	if err != nil {
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return nil, errors.New("receive timeout")
		}
		if errors.Is(err, os.ErrDeadlineExceeded) {
			return nil, errors.New("receive timeout")
		}
		return nil, err
	}
	return msg, nil
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

func endpointFromInstance(inst registry.ServiceInstance, localNodeID string) (transport.ServiceEndpoint, error) {
	endpoint := transport.ServiceEndpoint{Name: inst.Name}
	endpoint.Local = localNodeID != "" && inst.Node == localNodeID
	for _, ep := range inst.Endpoints {
		switch ep.Type {
		case registry.EndpointUDS:
			endpoint.UDSAddr = ep.Addr
		case registry.EndpointTCP:
			endpoint.TCPAddr = ep.Addr
		}
	}
	if endpoint.UDSAddr == "" && endpoint.TCPAddr == "" {
		return transport.ServiceEndpoint{}, errors.New("instance has no endpoints")
	}
	return endpoint, nil
}

type listenerGroup struct {
	listeners []transport.Listener
}

func (l *listenerGroup) Accept(context.Context) (transport.Conn, error) {
	return nil, errors.New("listener group does not support direct accept")
}

func (l *listenerGroup) Close() error {
	var joined error
	for _, listener := range l.listeners {
		joined = errors.Join(joined, listener.Close())
	}
	return joined
}

func (l *listenerGroup) Addr() string {
	if len(l.listeners) == 0 {
		return ""
	}
	return l.listeners[0].Addr()
}
