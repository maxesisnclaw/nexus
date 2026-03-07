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

type clientState int

const (
	clientNew clientState = iota
	clientServing
	clientClosed
)

type registryBackend interface {
	Register(inst registry.ServiceInstance) error
	Unregister(id string)
	Heartbeat(id string) bool
	Lookup(name string) []registry.ServiceInstance
	Watch(name string, cb func(registry.ChangeEvent)) (unsubscribe func())
	NodeID() string
	Close() error
}

type localRegistryBackend struct {
	reg *registry.Registry
}

func (b *localRegistryBackend) Register(inst registry.ServiceInstance) error {
	return b.reg.Register(inst)
}

func (b *localRegistryBackend) Unregister(id string) {
	b.reg.Unregister(id)
}

func (b *localRegistryBackend) Heartbeat(id string) bool {
	return b.reg.Heartbeat(id)
}

func (b *localRegistryBackend) Lookup(name string) []registry.ServiceInstance {
	return b.reg.Lookup(name)
}

func (b *localRegistryBackend) Watch(name string, cb func(registry.ChangeEvent)) (unsubscribe func()) {
	return b.reg.Watch(name, cb)
}

func (b *localRegistryBackend) NodeID() string {
	return b.reg.NodeID()
}

func (b *localRegistryBackend) Close() error {
	b.reg.Close()
	return nil
}

type roundRobinDiscovery struct {
	registry registryBackend
	mu       sync.Mutex
	offset   map[string]int
}

func newRoundRobinDiscovery(reg registryBackend) *roundRobinDiscovery {
	return &roundRobinDiscovery{
		registry: reg,
		offset:   make(map[string]int),
	}
}

func (d *roundRobinDiscovery) Pick(name string) (registry.ServiceInstance, error) {
	items := d.registry.Lookup(name)
	if len(items) == 0 {
		return registry.ServiceInstance{}, fmt.Errorf("service %q not found", name)
	}
	d.mu.Lock()
	idx := d.offset[name] % len(items)
	d.offset[name] = (idx + 1) % len(items)
	d.mu.Unlock()
	return items[idx], nil
}

// BusinessError wraps errors returned by the remote handler (not transport failures).
type BusinessError struct {
	Message string
}

func (e *BusinessError) Error() string {
	return e.Message
}

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
	// MaxInboundConns bounds concurrently served inbound connections.
	MaxInboundConns int
	// Registry is the service registry backend.
	Registry *registry.Registry
	// RegistryAddr points to daemon control socket for cross-process discovery.
	// When set and Registry is nil, SDK uses remote registry over this socket.
	RegistryAddr string
	// Router is the transport router used for outbound dials.
	// Serve/listen paths use built-in UDS/TCP transports based on Network.
	Router *transport.Router
	// Logger receives SDK logs.
	Logger *slog.Logger
	// AuthFunc is an optional hook called before dispatching each request.
	// Return a non-nil error to reject the request.
	// When nil, all requests are accepted (default: no auth).
	AuthFunc func(req *Request) error
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
	regAPI    registryBackend
	ownsReg   bool
	discovery *roundRobinDiscovery
	connPool  connectionPool

	handlers map[string]Handler

	localNodeID string

	mu         sync.RWMutex
	state      clientState
	listener   transport.Listener
	heartbeat  chan struct{}
	registered bool
	closeOnce  sync.Once
	closeErr   error

	activeConns   map[transport.Conn]struct{}
	activeConnsMu sync.Mutex
	activeWg      sync.WaitGroup
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
	if cfg.MaxInboundConns <= 0 {
		cfg.MaxInboundConns = 128
	}
	if cfg.Network == "" {
		cfg.Network = "uds"
	}
	if cfg.Router == nil {
		cfg.Router = transport.NewRouter(transport.NewUDSTransport(), transport.NewTCPTransport())
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	localNodeID := detectLocalNodeID()

	var localReg *registry.Registry
	var regAPI registryBackend
	ownsReg := false
	if cfg.Registry != nil {
		localReg = cfg.Registry
		regAPI = &localRegistryBackend{reg: cfg.Registry}
		localNodeID = cfg.Registry.NodeID()
	} else if cfg.RegistryAddr != "" {
		remote := newRegistryClient(cfg.RegistryAddr, localNodeID, logger)
		regAPI = remote
		ownsReg = true
	} else {
		localReg = registry.New(localNodeID)
		regAPI = &localRegistryBackend{reg: localReg}
		ownsReg = true
	}

	return &Client{
		cfg:         cfg,
		logger:      logger,
		registry:    localReg,
		regAPI:      regAPI,
		ownsReg:     ownsReg,
		discovery:   newRoundRobinDiscovery(regAPI),
		connPool:    newConnectionPool(cfg.Router),
		handlers:    make(map[string]Handler),
		heartbeat:   make(chan struct{}),
		localNodeID: localNodeID,
		activeConns: make(map[transport.Conn]struct{}),
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

// CallContext invokes a method with caller-provided context for cancellation and deadlines.
func (c *Client) CallContext(ctx context.Context, serviceName, method string, payload []byte) (*Response, error) {
	return c.callWithRetryCtx(ctx, serviceName, method, payload, false)
}

// CallWithData attempts fd-based transfer on local UDS and falls back to Call.
func (c *Client) CallWithData(serviceName, method string, payload []byte) (*Response, error) {
	if len(payload) < c.cfg.LargePayloadThreshold {
		return c.callWithRetry(serviceName, method, payload, false)
	}
	return c.callWithRetry(serviceName, method, payload, true)
}

// CallWithDataContext attempts fd-based transfer with caller-provided context.
func (c *Client) CallWithDataContext(ctx context.Context, serviceName, method string, payload []byte) (*Response, error) {
	if len(payload) < c.cfg.LargePayloadThreshold {
		return c.callWithRetryCtx(ctx, serviceName, method, payload, false)
	}
	return c.callWithRetryCtx(ctx, serviceName, method, payload, true)
}

func (c *Client) callWithRetry(serviceName, method string, payload []byte, preferFD bool) (*Response, error) {
	var lastErr error
	for attempt := 0; attempt <= c.cfg.CallRetries; attempt++ {
		resp, err := c.callOnce(serviceName, method, payload, preferFD)
		if err == nil {
			return resp, nil
		}
		var bizErr *BusinessError
		if errors.As(err, &bizErr) {
			return nil, err
		}
		lastErr = err
		if attempt == c.cfg.CallRetries {
			break
		}
		time.Sleep(c.cfg.RetryBackoff)
	}
	return nil, lastErr
}

func (c *Client) callWithRetryCtx(ctx context.Context, serviceName, method string, payload []byte, preferFD bool) (*Response, error) {
	var lastErr error
	for attempt := 0; attempt <= c.cfg.CallRetries; attempt++ {
		resp, err := c.callOnceCtx(ctx, serviceName, method, payload, preferFD)
		if err == nil {
			return resp, nil
		}
		var bizErr *BusinessError
		if errors.As(err, &bizErr) {
			return nil, err
		}
		lastErr = err
		if attempt == c.cfg.CallRetries {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(c.cfg.RetryBackoff):
		}
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
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetReadDeadline(deadline)
		_ = conn.SetWriteDeadline(deadline)
	}
	reusable := true
	defer func() {
		_ = conn.SetReadDeadline(time.Time{})
		_ = conn.SetWriteDeadline(time.Time{})
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
		return nil, &BusinessError{Message: msgErr}
	}
	return &Response{Payload: resp.Payload, Headers: resp.Headers}, nil
}

func (c *Client) callOnceCtx(ctx context.Context, serviceName, method string, payload []byte, preferFD bool) (*Response, error) {
	inst, err := c.discovery.Pick(serviceName)
	if err != nil {
		return nil, err
	}
	endpoint, err := endpointFromInstance(inst, c.localNodeID)
	if err != nil {
		return nil, err
	}
	reqCtx, cancel := context.WithTimeout(ctx, c.cfg.RequestTimeout)
	defer cancel()
	conn, err := c.connPool.Acquire(reqCtx, endpoint)
	if err != nil {
		return nil, err
	}
	if deadline, ok := reqCtx.Deadline(); ok {
		_ = conn.SetReadDeadline(deadline)
		_ = conn.SetWriteDeadline(deadline)
	}
	reusable := true
	defer func() {
		_ = conn.SetReadDeadline(time.Time{})
		_ = conn.SetWriteDeadline(time.Time{})
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
		return c.callMsgpackFallbackCtx(reqCtx, endpoint, method, payload)
	}
	ack, err := conn.Recv()
	if err != nil || ack.Headers[fdReadyKey] != "1" {
		reusable = false
		return c.callMsgpackFallbackCtx(reqCtx, endpoint, method, payload)
	}
	if err := conn.SendFd(fd, []byte("fd")); err != nil {
		reusable = false
		return c.callMsgpackFallbackCtx(reqCtx, endpoint, method, payload)
	}
	resp, err := conn.Recv()
	if err != nil {
		reusable = false
		return nil, err
	}
	if msgErr, ok := resp.Headers["error"]; ok {
		return nil, &BusinessError{Message: msgErr}
	}
	return &Response{Payload: resp.Payload, Headers: resp.Headers}, nil
}

func (c *Client) callMsgpackFallback(endpoint transport.ServiceEndpoint, method string, payload []byte) (*Response, error) {
	return c.callMsgpackFallbackCtx(context.Background(), endpoint, method, payload)
}

func (c *Client) callMsgpackFallbackCtx(ctx context.Context, endpoint transport.ServiceEndpoint, method string, payload []byte) (*Response, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.RequestTimeout)
	defer cancel()
	conn, err := c.connPool.Acquire(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetReadDeadline(deadline)
		_ = conn.SetWriteDeadline(deadline)
	}
	connHealthy := true
	defer func() {
		_ = conn.SetReadDeadline(time.Time{})
		_ = conn.SetWriteDeadline(time.Time{})
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
		return nil, true, &BusinessError{Message: msgErr}
	}
	return &Response{Payload: resp.Payload, Headers: resp.Headers}, true, nil
}

// Serve starts serving requests from configured endpoint.
func (c *Client) Serve(ctx context.Context) error {
	c.mu.Lock()
	state := c.state
	if state != clientNew {
		c.mu.Unlock()
		if state == clientClosed {
			return errors.New("client is closed")
		}
		return errors.New("client is already serving")
	}
	c.state = clientServing
	c.mu.Unlock()

	listeners, endpoints, err := c.listen(ctx)
	if err != nil {
		c.mu.Lock()
		if c.state == clientServing {
			c.state = clientNew
		}
		c.mu.Unlock()
		return err
	}
	listener := &listenerGroup{listeners: listeners}
	c.mu.Lock()
	c.listener = listener
	c.mu.Unlock()

	c.register(endpoints)
	defer c.Close()
	go c.heartbeatLoop()
	go func() {
		<-ctx.Done()
		_ = c.Close()
	}()

	type acceptResult struct {
		conn transport.Conn
		err  error
	}
	acceptCh := make(chan acceptResult, len(listeners))
	connSem := make(chan struct{}, c.cfg.MaxInboundConns)
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
				c.mu.RLock()
				closed := c.state == clientClosed
				c.mu.RUnlock()
				if closed {
					return nil
				}
				return result.err
			}
			select {
			case connSem <- struct{}{}:
				c.activeConnsMu.Lock()
				c.activeConns[result.conn] = struct{}{}
				c.activeConnsMu.Unlock()
				c.activeWg.Add(1)
				go func(conn transport.Conn) {
					defer func() { <-connSem }()
					defer func() {
						c.activeConnsMu.Lock()
						delete(c.activeConns, conn)
						c.activeConnsMu.Unlock()
						c.activeWg.Done()
					}()
					c.serveConn(conn)
				}(result.conn)
			default:
				c.logger.Warn("max inbound connections reached, rejecting", "limit", c.cfg.MaxInboundConns)
				_ = result.conn.Close()
			}
		}
	}
}

// Close unregisters this instance and closes server listener.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.state = clientClosed
		if c.registered {
			c.regAPI.Unregister(c.cfg.ID)
			c.registered = false
		}
		close(c.heartbeat)
		if c.listener != nil {
			c.closeErr = c.listener.Close()
			c.listener = nil
		}
		c.mu.Unlock()

		c.activeConnsMu.Lock()
		for conn := range c.activeConns {
			_ = conn.Close()
		}
		c.activeConnsMu.Unlock()

		c.mu.Lock()
		poolErr := c.connPool.Close()
		if c.closeErr != nil || poolErr != nil {
			c.closeErr = errors.Join(c.closeErr, poolErr)
		}
		if c.ownsReg {
			closeErr := c.regAPI.Close()
			if c.closeErr != nil || closeErr != nil {
				c.closeErr = errors.Join(c.closeErr, closeErr)
			}
		}
		c.mu.Unlock()
	})
	c.activeWg.Wait()
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
	if err := c.regAPI.Register(inst); err != nil {
		c.logger.Error("failed to register service instance", "name", inst.Name, "id", inst.ID, "err", err)
		return
	}
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
			_ = c.regAPI.Heartbeat(c.cfg.ID)
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
		if c.cfg.UDSAddr == "" {
			return nil, nil, errors.New("uds_addr is required for network mode \"uds\"")
		}
		if err := addListener(transport.NewUDSTransport(), registry.EndpointUDS, c.cfg.UDSAddr); err != nil {
			return nil, nil, err
		}
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
			if c.cfg.ServeTimeout > 0 {
				_ = conn.SetReadDeadline(time.Now().Add(c.cfg.ServeTimeout))
			}
			fd, _, err := conn.RecvFd()
			_ = conn.SetReadDeadline(time.Time{})
			if err != nil {
				if err := conn.Send(&transport.Message{Headers: map[string]string{"error": err.Error()}}); err != nil {
					c.logger.Debug("send failed", "err", err)
					return
				}
				continue
			}
			data, err := transport.ReadFDAll(fd, 64<<20) // 64 MiB, matches msgpack transport limit
			_ = syscall.Close(fd)
			if err != nil {
				if err := conn.Send(&transport.Message{Headers: map[string]string{"error": err.Error()}}); err != nil {
					c.logger.Debug("send failed", "err", err)
					return
				}
				continue
			}
			if req.Headers == nil {
				req.Headers = map[string]string{}
			}
			req.Method = req.Headers["method"]
			req.Payload = data
		}

		if c.cfg.AuthFunc != nil {
			if err := c.cfg.AuthFunc(req); err != nil {
				if err := conn.Send(&transport.Message{Headers: map[string]string{"error": err.Error()}}); err != nil {
					c.logger.Debug("send failed", "err", err)
					return
				}
				continue
			}
		}

		resp, err := c.dispatch(req)
		if err != nil {
			if err := conn.Send(&transport.Message{Headers: map[string]string{"error": err.Error()}}); err != nil {
				c.logger.Debug("send failed", "err", err)
				return
			}
			continue
		}
		if err := conn.Send(&transport.Message{Method: req.Method, Payload: resp.Payload, Headers: resp.Headers}); err != nil {
			c.logger.Debug("send failed", "err", err)
			return
		}
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

func (c *Client) dispatch(req *Request) (resp *Response, err error) {
	defer func() {
		if r := recover(); r != nil {
			c.logger.Error("handler panic", "method", req.Method, "panic", r)
			resp = nil
			err = fmt.Errorf("handler panic: %v", r)
		}
	}()
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

func detectLocalNodeID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "local"
	}
	return host
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
