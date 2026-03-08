package daemon

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/maxesisn/nexus/pkg/registry"
	"github.com/vmihailenco/msgpack/v5"
)

// Control-plane messages are expected to stay small. Keep a strict cap so one
// connection cannot force large frame allocations.
const maxControlMessageSize = 1 * 1024 * 1024
const maxControlConns = 64
const controlReadTimeout = 5 * time.Minute

type controlRequest struct {
	Cmd          string              `msgpack:"cmd"`
	Name         string              `msgpack:"name,omitempty"`
	ID           string              `msgpack:"id,omitempty"`
	Endpoints    []registry.Endpoint `msgpack:"endpoints,omitempty"`
	Capabilities []string            `msgpack:"capabilities,omitempty"`
	TTLMS        int64               `msgpack:"ttl_ms,omitempty"`
}

type statusService struct {
	Name    string `msgpack:"name"`
	ID      string `msgpack:"id"`
	PID     int    `msgpack:"pid"`
	Running bool   `msgpack:"running"`
}

type healthResponse struct {
	OK            bool  `msgpack:"ok"`
	UptimeSeconds int64 `msgpack:"uptime_seconds"`
}

type okResponse struct {
	OK bool `msgpack:"ok"`
}

type lookupResponse struct {
	Instances []registry.ServiceInstance `msgpack:"instances"`
}

type statusResponse struct {
	Services []statusService `msgpack:"services"`
}

type controlErrorResponse struct {
	OK    bool   `msgpack:"ok"`
	Error string `msgpack:"error"`
}

type watchEventResponse struct {
	Event    string                   `msgpack:"event"`
	Instance registry.ServiceInstance `msgpack:"instance"`
}

// ControlServer handles daemon control-plane requests over a UDS socket.
type ControlServer struct {
	listener  net.Listener
	daemon    *Daemon
	registry  *registry.Registry
	logger    *slog.Logger
	startedAt time.Time
	connSem   chan struct{}

	mu         sync.Mutex
	conns      map[net.Conn]struct{}
	closed     bool
	socketPath string
	wg         sync.WaitGroup
}

// NewControlServer creates a control server instance.
func NewControlServer(daemon *Daemon, reg *registry.Registry, logger *slog.Logger, startedAt time.Time) *ControlServer {
	if logger == nil {
		logger = slog.Default()
	}
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	return &ControlServer{
		daemon:    daemon,
		registry:  reg,
		logger:    logger,
		startedAt: startedAt,
		connSem:   make(chan struct{}, maxControlConns),
		conns:     make(map[net.Conn]struct{}),
	}
}

// Start binds the unix socket and starts accepting control connections.
func (s *ControlServer) Start(socketPath string) error {
	if socketPath == "" {
		return errors.New("daemon.socket is required")
	}
	if s.daemon == nil {
		return errors.New("control server daemon is nil")
	}
	if s.registry == nil {
		return errors.New("control server registry is nil")
	}
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		return fmt.Errorf("create control socket directory: %w", err)
	}
	if err := cleanupControlSocketPath(socketPath); err != nil {
		return err
	}
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen control socket %s: %w", socketPath, err)
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		_ = ln.Close()
		return fmt.Errorf("chmod control socket: %w", err)
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = ln.Close()
		return errors.New("control server already closed")
	}
	s.listener = ln
	s.socketPath = socketPath
	s.mu.Unlock()

	s.wg.Add(1)
	go s.acceptLoop()
	return nil
}

func (s *ControlServer) acceptLoop() {
	defer s.wg.Done()
	s.mu.Lock()
	ln := s.listener
	s.mu.Unlock()
	if ln == nil {
		return
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			if s.isClosed() || errors.Is(err, net.ErrClosed) {
				return
			}
			s.logger.Warn("control accept failed", "err", err)
			continue
		}

		select {
		case s.connSem <- struct{}{}:
		default:
			s.logger.Warn("control connection limit reached", "max", maxControlConns)
			_ = conn.Close()
			continue
		}

		if !s.trackConn(conn) {
			<-s.connSem
			continue
		}
		s.wg.Add(1)
		go s.serveConn(conn)
	}
}

func (s *ControlServer) serveConn(conn net.Conn) {
	defer s.wg.Done()
	defer func() { <-s.connSem }()
	session := &controlSession{server: s, conn: conn}
	defer session.Close()

	for {
		if err := session.extendReadDeadline(); err != nil {
			return
		}
		var req controlRequest
		if err := readControlMessage(conn, &req); err != nil {
			if !errors.Is(err, io.EOF) {
				s.logger.Debug("control connection closed", "err", err)
			}
			return
		}
		if err := session.handleRequest(req); err != nil {
			if sendErr := session.send(controlErrorResponse{OK: false, Error: err.Error()}); sendErr != nil {
				s.logger.Debug("control error response failed", "err", sendErr)
				return
			}
		}
	}
}

// Close stops the control server and all active client sessions.
func (s *ControlServer) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	ln := s.listener
	s.listener = nil
	socketPath := s.socketPath
	s.socketPath = ""
	conns := make([]net.Conn, 0, len(s.conns))
	for conn := range s.conns {
		conns = append(conns, conn)
	}
	s.conns = make(map[net.Conn]struct{})
	s.mu.Unlock()

	var joined error
	if ln != nil {
		if err := ln.Close(); err != nil && !isClosedNetworkError(err) {
			joined = errors.Join(joined, err)
		}
	}
	for _, conn := range conns {
		if err := conn.Close(); err != nil && !isClosedNetworkError(err) {
			joined = errors.Join(joined, err)
		}
	}
	s.wg.Wait()
	if socketPath != "" {
		if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			joined = errors.Join(joined, fmt.Errorf("remove control socket: %w", err))
		}
	}
	return joined
}

func (s *ControlServer) trackConn(conn net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		_ = conn.Close()
		return false
	}
	s.conns[conn] = struct{}{}
	return true
}

func (s *ControlServer) untrackConn(conn net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.conns, conn)
}

func (s *ControlServer) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

type controlSession struct {
	server *ControlServer
	conn   net.Conn

	writeMu sync.Mutex
	watchMu sync.Mutex
	// hasWatches disables the short read timeout for long-lived watch streams.
	hasWatches bool
	unsubs     []func()
	once       sync.Once
}

func (c *controlSession) Close() {
	c.once.Do(func() {
		c.watchMu.Lock()
		unsubs := append([]func(){}, c.unsubs...)
		c.unsubs = nil
		c.watchMu.Unlock()
		for _, unsub := range unsubs {
			unsub()
		}
		c.server.untrackConn(c.conn)
		_ = c.conn.Close()
	})
}

func (c *controlSession) handleRequest(req controlRequest) error {
	switch req.Cmd {
	case "status":
		states := c.server.daemon.ProcessStates()
		services := make([]statusService, 0, len(states))
		for _, st := range states {
			services = append(services, statusService{
				Name:    st.Service,
				ID:      st.ID,
				PID:     st.PID,
				Running: st.Running,
			})
		}
		return c.send(statusResponse{Services: services})
	case "health":
		uptime := int64(time.Since(c.server.startedAt).Seconds())
		if uptime < 0 {
			uptime = 0
		}
		return c.send(healthResponse{OK: true, UptimeSeconds: uptime})
	case "register":
		if req.Name == "" || req.ID == "" {
			return errors.New("register requires name and id")
		}
		inst := registry.ServiceInstance{
			Name:         req.Name,
			ID:           req.ID,
			Endpoints:    append([]registry.Endpoint(nil), req.Endpoints...),
			Capabilities: append([]string(nil), req.Capabilities...),
		}
		if req.TTLMS > 0 {
			inst.TTL = time.Duration(req.TTLMS) * time.Millisecond
		}
		if err := c.server.registry.Register(inst); err != nil {
			return err
		}
		return c.send(okResponse{OK: true})
	case "unregister":
		if req.ID == "" {
			return errors.New("unregister requires id")
		}
		c.server.registry.Unregister(req.ID)
		return c.send(okResponse{OK: true})
	case "heartbeat":
		if req.ID == "" {
			return errors.New("heartbeat requires id")
		}
		if ok := c.server.registry.Heartbeat(req.ID); !ok {
			return c.send(controlErrorResponse{OK: false, Error: "instance not found"})
		}
		return c.send(okResponse{OK: true})
	case "lookup":
		if req.Name == "" {
			return errors.New("lookup requires name")
		}
		instances := c.server.registry.Lookup(req.Name)
		return c.send(lookupResponse{Instances: instances})
	case "watch":
		if req.Name == "" {
			return errors.New("watch requires name")
		}
		c.hasWatches = true
		unsub := c.server.registry.Watch(req.Name, func(event registry.ChangeEvent) {
			if err := c.send(watchEventResponse{Event: string(event.Type), Instance: event.Instance}); err != nil {
				_ = c.conn.Close()
			}
		})
		c.watchMu.Lock()
		c.unsubs = append(c.unsubs, unsub)
		c.watchMu.Unlock()
		return c.send(okResponse{OK: true})
	default:
		return fmt.Errorf("unknown command: %s", req.Cmd)
	}
}

func (c *controlSession) send(msg any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return writeControlMessage(c.conn, msg)
}

func (c *controlSession) extendReadDeadline() error {
	if c.hasWatches {
		return c.conn.SetReadDeadline(time.Time{})
	}
	return c.conn.SetReadDeadline(time.Now().Add(controlReadTimeout))
}

func cleanupControlSocketPath(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat existing control socket: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("control socket path exists and is not a socket: %s", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale control socket: %w", err)
	}
	return nil
}

func writeControlMessage(w io.Writer, msg any) error {
	body, err := msgpack.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal control message: %w", err)
	}
	if len(body) > maxControlMessageSize {
		return fmt.Errorf("control message too large: %d", len(body))
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(body)))
	if err := writeAll(w, hdr[:]); err != nil {
		return fmt.Errorf("write control header: %w", err)
	}
	if err := writeAll(w, body); err != nil {
		return fmt.Errorf("write control body: %w", err)
	}
	if err := flushWriter(w); err != nil {
		return fmt.Errorf("flush control writer: %w", err)
	}
	return nil
}

func readControlMessage(r io.Reader, out any) error {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return err
	}
	size := binary.BigEndian.Uint32(hdr[:])
	if size > maxControlMessageSize {
		return fmt.Errorf("control message exceeds maximum size of %d bytes", maxControlMessageSize)
	}
	buf := make([]byte, size)
	if _, err := io.ReadFull(r, buf); err != nil {
		return err
	}
	if err := msgpack.Unmarshal(buf, out); err != nil {
		return fmt.Errorf("decode control message: %w", err)
	}
	return nil
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func flushWriter(w io.Writer) error {
	type flusher interface {
		Flush() error
	}
	if fw, ok := w.(flusher); ok {
		return fw.Flush()
	}
	return nil
}

func isClosedNetworkError(err error) bool {
	return errors.Is(err, net.ErrClosed) || strings.Contains(err.Error(), "use of closed network connection")
}
