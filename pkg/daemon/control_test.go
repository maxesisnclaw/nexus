package daemon

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/maxesisn/nexus/pkg/config"
	"github.com/maxesisn/nexus/pkg/registry"
)

func TestControlServerStatus(t *testing.T) {
	d, cancel, cleanup := startTestDaemonWithSocket(t)
	defer cleanup()
	defer cancel()

	conn := dialControlSocket(t, d.cfg.Daemon.Socket)
	defer conn.Close()

	if err := writeControlMessage(conn, controlRequest{Cmd: "status"}); err != nil {
		t.Fatalf("write status request: %v", err)
	}
	var resp statusResponse
	if err := readControlMessage(conn, &resp); err != nil {
		t.Fatalf("read status response: %v", err)
	}
	if len(resp.Services) != 0 {
		t.Fatalf("expected no services, got %+v", resp.Services)
	}
}

func TestControlServerRegisterAndLookup(t *testing.T) {
	d, cancel, cleanup := startTestDaemonWithSocket(t)
	defer cleanup()
	defer cancel()

	conn := dialControlSocket(t, d.cfg.Daemon.Socket)
	defer conn.Close()

	err := writeControlMessage(conn, controlRequest{
		Cmd:       "register",
		Name:      "svc",
		ID:        "svc-1",
		Endpoints: []registry.Endpoint{{Type: registry.EndpointUDS, Addr: "/tmp/svc.sock"}},
	})
	if err != nil {
		t.Fatalf("write register request: %v", err)
	}
	var registerResp okResponse
	if err := readControlMessage(conn, &registerResp); err != nil {
		t.Fatalf("read register response: %v", err)
	}
	if !registerResp.OK {
		t.Fatal("expected register ok=true")
	}

	if err := writeControlMessage(conn, controlRequest{Cmd: "lookup", Name: "svc"}); err != nil {
		t.Fatalf("write lookup request: %v", err)
	}
	var lookupResp lookupResponse
	if err := readControlMessage(conn, &lookupResp); err != nil {
		t.Fatalf("read lookup response: %v", err)
	}
	if len(lookupResp.Instances) != 1 {
		t.Fatalf("expected one instance, got %+v", lookupResp.Instances)
	}
	if lookupResp.Instances[0].ID != "svc-1" {
		t.Fatalf("unexpected instance: %+v", lookupResp.Instances[0])
	}
}

func TestControlServerWatchReceivesUpEvent(t *testing.T) {
	d, cancel, cleanup := startTestDaemonWithSocket(t)
	defer cleanup()
	defer cancel()

	watchConn := dialControlSocket(t, d.cfg.Daemon.Socket)
	defer watchConn.Close()

	if err := writeControlMessage(watchConn, controlRequest{Cmd: "watch", Name: "svc"}); err != nil {
		t.Fatalf("write watch request: %v", err)
	}
	var watchAck okResponse
	if err := readControlMessage(watchConn, &watchAck); err != nil {
		t.Fatalf("read watch ack: %v", err)
	}
	if !watchAck.OK {
		t.Fatal("expected watch ack ok=true")
	}

	registerConn := dialControlSocket(t, d.cfg.Daemon.Socket)
	defer registerConn.Close()
	if err := writeControlMessage(registerConn, controlRequest{
		Cmd:       "register",
		Name:      "svc",
		ID:        "svc-watch-1",
		Endpoints: []registry.Endpoint{{Type: registry.EndpointUDS, Addr: "/tmp/svc-watch.sock"}},
	}); err != nil {
		t.Fatalf("write register request: %v", err)
	}
	var registerResp okResponse
	if err := readControlMessage(registerConn, &registerResp); err != nil {
		t.Fatalf("read register response: %v", err)
	}
	if !registerResp.OK {
		t.Fatal("expected register ok=true")
	}

	_ = watchConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	defer watchConn.SetReadDeadline(time.Time{})
	var event watchEventResponse
	if err := readControlMessage(watchConn, &event); err != nil {
		t.Fatalf("read watch event: %v", err)
	}
	if event.Event != string(registry.ChangeUp) {
		t.Fatalf("unexpected event type: %s", event.Event)
	}
	if event.Instance.ID != "svc-watch-1" {
		t.Fatalf("unexpected watch instance: %+v", event.Instance)
	}
}

func TestControlServerHeartbeatPreventsExpiry(t *testing.T) {
	d, cancel, cleanup := startTestDaemonWithSocket(t)
	defer cleanup()
	defer cancel()

	conn := dialControlSocket(t, d.cfg.Daemon.Socket)
	defer conn.Close()

	if err := writeControlMessage(conn, controlRequest{
		Cmd:       "register",
		Name:      "svc",
		ID:        "svc-heartbeat-1",
		TTLMS:     150,
		Endpoints: []registry.Endpoint{{Type: registry.EndpointUDS, Addr: "/tmp/svc-heartbeat.sock"}},
	}); err != nil {
		t.Fatalf("write register request: %v", err)
	}
	var registerResp okResponse
	if err := readControlMessage(conn, &registerResp); err != nil {
		t.Fatalf("read register response: %v", err)
	}
	if !registerResp.OK {
		t.Fatal("expected register ok=true")
	}

	for i := 0; i < 10; i++ {
		time.Sleep(120 * time.Millisecond)
		if err := writeControlMessage(conn, controlRequest{Cmd: "heartbeat", ID: "svc-heartbeat-1"}); err != nil {
			t.Fatalf("write heartbeat request: %v", err)
		}
		var heartbeatResp okResponse
		if err := readControlMessage(conn, &heartbeatResp); err != nil {
			t.Fatalf("read heartbeat response: %v", err)
		}
		if !heartbeatResp.OK {
			t.Fatal("expected heartbeat ok=true")
		}
	}

	if err := writeControlMessage(conn, controlRequest{Cmd: "lookup", Name: "svc"}); err != nil {
		t.Fatalf("write lookup request: %v", err)
	}
	var lookupResp lookupResponse
	if err := readControlMessage(conn, &lookupResp); err != nil {
		t.Fatalf("read lookup response: %v", err)
	}
	if len(lookupResp.Instances) != 1 {
		t.Fatalf("expected alive instance after heartbeats, got %+v", lookupResp.Instances)
	}
}

func TestControlServerConcurrentClients(t *testing.T) {
	d, cancel, cleanup := startTestDaemonWithSocket(t)
	defer cleanup()
	defer cancel()

	const clients = 20
	var wg sync.WaitGroup
	errCh := make(chan error, clients)
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := net.Dial("unix", d.cfg.Daemon.Socket)
			if err != nil {
				errCh <- err
				return
			}
			defer conn.Close()
			if err := writeControlMessage(conn, controlRequest{Cmd: "health"}); err != nil {
				errCh <- err
				return
			}
			var resp healthResponse
			if err := readControlMessage(conn, &resp); err != nil {
				errCh <- err
				return
			}
			if !resp.OK {
				errCh <- io.ErrUnexpectedEOF
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent client failed: %v", err)
		}
	}
}

func TestControlServerConnectionLimit(t *testing.T) {
	d, cancel, cleanup := startTestDaemonWithSocket(t)
	defer cleanup()
	defer cancel()

	held := make([]net.Conn, 0, maxControlConns)
	defer func() {
		for _, conn := range held {
			_ = conn.Close()
		}
	}()

	for i := 0; i < maxControlConns; i++ {
		conn := dialControlSocket(t, d.cfg.Daemon.Socket)
		if err := writeControlMessage(conn, controlRequest{Cmd: "watch", Name: fmt.Sprintf("svc-%d", i)}); err != nil {
			_ = conn.Close()
			t.Fatalf("write watch request %d: %v", i, err)
		}
		var ack okResponse
		if err := readControlMessage(conn, &ack); err != nil {
			_ = conn.Close()
			t.Fatalf("read watch ack %d: %v", i, err)
		}
		if !ack.OK {
			_ = conn.Close()
			t.Fatalf("watch ack %d returned ok=false", i)
		}
		held = append(held, conn)
	}

	extraConn, err := net.Dial("unix", d.cfg.Daemon.Socket)
	if err != nil {
		t.Fatalf("dial extra control connection: %v", err)
	}
	defer extraConn.Close()

	_ = extraConn.SetWriteDeadline(time.Now().Add(200 * time.Millisecond))
	writeErr := writeControlMessage(extraConn, controlRequest{Cmd: "health"})
	_ = extraConn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	var resp healthResponse
	readErr := readControlMessage(extraConn, &resp)

	if writeErr == nil && readErr == nil {
		t.Fatalf("expected extra connection to be rejected when limit %d is reached", maxControlConns)
	}
}

func startTestDaemonWithSocket(t *testing.T) (*Daemon, context.CancelFunc, func()) {
	t.Helper()
	socket := testControlSocketPath(t, "daemon")
	cfg := &config.Config{
		Daemon: config.DaemonConfig{
			Socket:         socket,
			HealthInterval: config.Duration{Duration: 50 * time.Millisecond},
			ShutdownGrace:  config.Duration{Duration: 500 * time.Millisecond},
		},
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	d, err := New(cfg, logger)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := d.Start(ctx); err != nil {
		cancel()
		t.Fatalf("Start() error = %v", err)
	}
	cleanup := func() {
		if err := d.Stop(); err != nil {
			t.Fatalf("Stop() error = %v", err)
		}
	}
	return d, cancel, cleanup
}

func testControlSocketPath(t *testing.T, prefix string) string {
	t.Helper()
	path := filepath.Join("/tmp", fmt.Sprintf("nexus-%s-%d.sock", prefix, time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.Remove(path) })
	return path
}

func dialControlSocket(t *testing.T, addr string) net.Conn {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("unix", addr)
		if err == nil {
			return conn
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out dialing control socket %s", addr)
	return nil
}

type deadlineCaptureConn struct {
	lastReadDeadline time.Time
}

func (c *deadlineCaptureConn) Read(_ []byte) (int, error)         { return 0, io.EOF }
func (c *deadlineCaptureConn) Write(b []byte) (int, error)        { return len(b), nil }
func (c *deadlineCaptureConn) Close() error                       { return nil }
func (c *deadlineCaptureConn) LocalAddr() net.Addr                { return dummyAddr("local") }
func (c *deadlineCaptureConn) RemoteAddr() net.Addr               { return dummyAddr("remote") }
func (c *deadlineCaptureConn) SetDeadline(_ time.Time) error      { return nil }
func (c *deadlineCaptureConn) SetWriteDeadline(_ time.Time) error { return nil }
func (c *deadlineCaptureConn) SetReadDeadline(t time.Time) error {
	c.lastReadDeadline = t
	return nil
}

type dummyAddr string

func (a dummyAddr) Network() string { return "test" }
func (a dummyAddr) String() string  { return string(a) }

func TestControlSessionExtendReadDeadlinePolicy(t *testing.T) {
	conn := &deadlineCaptureConn{}
	session := &controlSession{conn: conn}

	if err := session.extendReadDeadline(); err != nil {
		t.Fatalf("extendReadDeadline() without watch error = %v", err)
	}
	got := conn.lastReadDeadline
	if got.IsZero() {
		t.Fatal("expected non-zero read deadline for non-watch session")
	}

	lowerBound := time.Now().Add(controlReadTimeout - 5*time.Second)
	upperBound := time.Now().Add(controlReadTimeout + 5*time.Second)
	if got.Before(lowerBound) || got.After(upperBound) {
		t.Fatalf("read deadline %v outside expected timeout window [%v, %v]", got, lowerBound, upperBound)
	}

	session.hasWatches = true
	if err := session.extendReadDeadline(); err != nil {
		t.Fatalf("extendReadDeadline() with watch error = %v", err)
	}
	if !conn.lastReadDeadline.IsZero() {
		t.Fatalf("expected zero read deadline for watch session, got %v", conn.lastReadDeadline)
	}
}
