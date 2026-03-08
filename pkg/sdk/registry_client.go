package sdk

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/maxesisn/nexus/pkg/registry"
	"github.com/vmihailenco/msgpack/v5"
)

const (
	registryClientDialTimeout = 2 * time.Second
	maxRegistryMessageSize    = 64 * 1024 * 1024
)

var registryClientIODeadline = 10 * time.Second

type registryClient struct {
	addr   string
	nodeID string
	logger *slog.Logger

	mu         sync.Mutex
	closed     bool
	nextWatch  int
	watchStops map[int]func()
}

type registryRequest struct {
	Cmd          string              `msgpack:"cmd"`
	Name         string              `msgpack:"name,omitempty"`
	ID           string              `msgpack:"id,omitempty"`
	Endpoints    []registry.Endpoint `msgpack:"endpoints,omitempty"`
	Capabilities []string            `msgpack:"capabilities,omitempty"`
	TTLMS        int64               `msgpack:"ttl_ms,omitempty"`
}

type controlReply struct {
	OK    bool   `msgpack:"ok"`
	Error string `msgpack:"error"`
}

type registryLookupReply struct {
	Instances []registry.ServiceInstance `msgpack:"instances"`
	Error     string                     `msgpack:"error"`
}

type registryWatchEvent struct {
	Event    string                   `msgpack:"event"`
	Instance registry.ServiceInstance `msgpack:"instance"`
}

func newRegistryClient(addr string, nodeID string, logger *slog.Logger) *registryClient {
	if logger == nil {
		logger = slog.Default()
	}
	return &registryClient{
		addr:       addr,
		nodeID:     nodeID,
		logger:     logger,
		watchStops: make(map[int]func()),
	}
}

func (c *registryClient) NodeID() string {
	return c.nodeID
}

func (c *registryClient) Register(inst registry.ServiceInstance) error {
	req := registryRequest{
		Cmd:          "register",
		Name:         inst.Name,
		ID:           inst.ID,
		Endpoints:    append([]registry.Endpoint(nil), inst.Endpoints...),
		Capabilities: append([]string(nil), inst.Capabilities...),
	}
	if inst.TTL > 0 {
		req.TTLMS = inst.TTL.Milliseconds()
	}
	var resp controlReply
	if err := c.request(req, &resp); err != nil {
		return err
	}
	if !resp.OK {
		if resp.Error == "" {
			resp.Error = "register failed"
		}
		return errors.New(resp.Error)
	}
	return nil
}

func (c *registryClient) Unregister(id string) {
	if id == "" {
		return
	}
	var resp controlReply
	err := c.request(registryRequest{Cmd: "unregister", ID: id}, &resp)
	if err != nil {
		c.logger.Debug("remote unregister failed", "id", id, "err", err)
		return
	}
	if !resp.OK {
		if resp.Error == "" {
			resp.Error = "unregister failed"
		}
		c.logger.Debug("remote unregister failed", "id", id, "err", resp.Error)
	}
}

func (c *registryClient) Heartbeat(id string) bool {
	if id == "" {
		return false
	}
	var resp controlReply
	if err := c.request(registryRequest{Cmd: "heartbeat", ID: id}, &resp); err != nil {
		c.logger.Debug("remote heartbeat failed", "id", id, "err", err)
		return false
	}
	if resp.Error != "" {
		return false
	}
	return resp.OK
}

func (c *registryClient) Lookup(name string) []registry.ServiceInstance {
	if name == "" {
		return nil
	}
	var resp registryLookupReply
	if err := c.request(registryRequest{Cmd: "lookup", Name: name}, &resp); err != nil {
		c.logger.Debug("remote lookup failed", "name", name, "err", err)
		return nil
	}
	if resp.Error != "" {
		c.logger.Debug("remote lookup failed", "name", name, "err", resp.Error)
		return nil
	}
	return resp.Instances
}

func (c *registryClient) Watch(name string, cb func(registry.ChangeEvent)) (unsubscribe func()) {
	noopUnsubscribe := func() {}
	if cb == nil {
		cb = func(registry.ChangeEvent) {}
	}
	if name == "" {
		return noopUnsubscribe
	}
	conn, err := c.dial()
	if err != nil {
		c.logger.Debug("remote watch dial failed", "name", name, "err", err)
		return noopUnsubscribe
	}
	if err := conn.SetDeadline(time.Now().Add(registryClientIODeadline)); err != nil {
		_ = conn.Close()
		c.logger.Debug("remote watch set deadline failed", "name", name, "err", err)
		return noopUnsubscribe
	}
	if err := writeRegistryMessage(conn, registryRequest{Cmd: "watch", Name: name}); err != nil {
		_ = conn.Close()
		c.logger.Debug("remote watch request failed", "name", name, "err", err)
		return noopUnsubscribe
	}
	var ack controlReply
	if err := readRegistryMessage(conn, &ack); err != nil {
		_ = conn.Close()
		c.logger.Debug("remote watch ack failed", "name", name, "err", err)
		return noopUnsubscribe
	}
	if !ack.OK {
		_ = conn.Close()
		if ack.Error == "" {
			ack.Error = "watch failed"
		}
		c.logger.Debug("remote watch rejected", "name", name, "err", ack.Error)
		return noopUnsubscribe
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		c.logger.Debug("remote watch clear deadline failed", "name", name, "err", err)
		return noopUnsubscribe
	}

	watchID, unregister := c.trackWatch(conn)
	go func() {
		defer unregister()
		for {
			var event registryWatchEvent
			if err := readRegistryMessage(conn, &event); err != nil {
				if !errors.Is(err, io.EOF) {
					c.logger.Debug("remote watch stream ended", "name", name, "err", err)
				}
				return
			}
			if event.Event == "" {
				continue
			}
			func() {
				defer func() { _ = recover() }()
				cb(registry.ChangeEvent{Type: registry.ChangeType(event.Event), Instance: event.Instance})
			}()
		}
	}()

	return func() {
		c.mu.Lock()
		stop := c.watchStops[watchID]
		c.mu.Unlock()
		if stop != nil {
			stop()
		}
	}
}

func (c *registryClient) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	stops := make([]func(), 0, len(c.watchStops))
	for _, stop := range c.watchStops {
		stops = append(stops, stop)
	}
	c.watchStops = map[int]func(){}
	c.mu.Unlock()

	for _, stop := range stops {
		stop()
	}
	return nil
}

func (c *registryClient) request(req registryRequest, resp any) error {
	conn, err := c.dial()
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(registryClientIODeadline)); err != nil {
		return fmt.Errorf("set registry request deadline: %w", err)
	}
	if err := writeRegistryMessage(conn, req); err != nil {
		return err
	}
	if err := readRegistryMessage(conn, resp); err != nil {
		return err
	}
	return nil
}

func (c *registryClient) dial() (net.Conn, error) {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return nil, errors.New("registry client is closed")
	}
	dialer := net.Dialer{Timeout: registryClientDialTimeout}
	conn, err := dialer.Dial("unix", c.addr)
	if err != nil {
		return nil, fmt.Errorf("dial registry socket %s: %w", c.addr, err)
	}
	return conn, nil
}

func (c *registryClient) trackWatch(conn net.Conn) (int, func()) {
	c.mu.Lock()
	id := c.nextWatch
	c.nextWatch++
	once := sync.Once{}
	stop := func() {
		once.Do(func() {
			_ = conn.Close()
			c.mu.Lock()
			delete(c.watchStops, id)
			c.mu.Unlock()
		})
	}
	if c.closed {
		c.mu.Unlock()
		stop()
		return id, func() {}
	}
	c.watchStops[id] = stop
	c.mu.Unlock()
	return id, stop
}

func writeRegistryMessage(w io.Writer, msg any) error {
	body, err := msgpack.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal registry message: %w", err)
	}
	if len(body) > maxRegistryMessageSize {
		return fmt.Errorf("registry message too large: %d", len(body))
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(body)))
	if err := writeAll(w, hdr[:]); err != nil {
		return fmt.Errorf("write registry header: %w", err)
	}
	if err := writeAll(w, body); err != nil {
		return fmt.Errorf("write registry body: %w", err)
	}
	if err := flushWriter(w); err != nil {
		return fmt.Errorf("flush registry writer: %w", err)
	}
	return nil
}

func readRegistryMessage(r io.Reader, out any) error {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return err
	}
	size := binary.BigEndian.Uint32(hdr[:])
	if size > maxRegistryMessageSize {
		return fmt.Errorf("registry message exceeds maximum size of %d bytes", maxRegistryMessageSize)
	}
	buf := make([]byte, size)
	if _, err := io.ReadFull(r, buf); err != nil {
		return err
	}
	if err := msgpack.Unmarshal(buf, out); err != nil {
		return fmt.Errorf("decode registry message: %w", err)
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
