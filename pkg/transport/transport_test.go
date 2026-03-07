package transport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUDSTransportSendRecv(t *testing.T) {
	ctx := context.Background()
	uds := NewUDSTransport()
	sock := testSocketPath(t, "echo")
	ln, err := uds.Listen(ctx, sock)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer ln.Close()
	if ln.Addr() == "" {
		t.Fatal("expected non-empty listener address")
	}

	done := make(chan error, 1)
	go func() {
		serverConn, err := ln.Accept(ctx)
		if err != nil {
			done <- err
			return
		}
		defer serverConn.Close()
		msg, err := serverConn.Recv()
		if err != nil {
			done <- err
			return
		}
		if msg.Method != "ping" {
			done <- errUnexpectedMethod(msg.Method)
			return
		}
		done <- serverConn.Send(&Message{Method: "pong", Payload: []byte("ok")})
	}()

	clientConn, err := uds.Dial(ctx, ServiceEndpoint{UDSAddr: sock, Local: true})
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	defer clientConn.Close()
	if err := clientConn.Send(&Message{Method: "ping", Payload: []byte("hello")}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	resp, err := clientConn.Recv()
	if err != nil {
		t.Fatalf("Recv() error = %v", err)
	}
	if resp.Method != "pong" {
		t.Fatalf("unexpected method: %s", resp.Method)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("server loop error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server loop timeout")
	}
}

func TestTCPTransportSendRecv(t *testing.T) {
	ctx := context.Background()
	tcp := NewTCPTransport()
	ln, err := tcp.Listen(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer ln.Close()
	if ln.Addr() == "" {
		t.Fatal("expected non-empty listener address")
	}

	done := make(chan error, 1)
	go func() {
		serverConn, err := ln.Accept(ctx)
		if err != nil {
			done <- err
			return
		}
		defer serverConn.Close()
		msg, err := serverConn.Recv()
		if err != nil {
			done <- err
			return
		}
		if msg.Method != "ping" {
			done <- errUnexpectedMethod(msg.Method)
			return
		}
		done <- serverConn.Send(&Message{Method: "pong"})
	}()

	clientConn, err := tcp.Dial(ctx, ServiceEndpoint{TCPAddr: ln.Addr()})
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	defer clientConn.Close()
	if err := clientConn.Send(&Message{Method: "ping"}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	resp, err := clientConn.Recv()
	if err != nil {
		t.Fatalf("Recv() error = %v", err)
	}
	if resp.Method != "pong" {
		t.Fatalf("unexpected method: %s", resp.Method)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("server loop error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server loop timeout")
	}
}

func TestRouterDialSelection(t *testing.T) {
	uds := &fakeTransport{}
	tcp := &fakeTransport{}
	r := NewRouter(uds, tcp)

	if _, err := r.Dial(context.Background(), ServiceEndpoint{Name: "a", Local: true, UDSAddr: "/tmp/a.sock", TCPAddr: "10.0.0.1:1"}); err != nil {
		t.Fatalf("Dial local error = %v", err)
	}
	if uds.dials != 1 || tcp.dials != 0 {
		t.Fatalf("unexpected dial counts uds=%d tcp=%d", uds.dials, tcp.dials)
	}
	if _, err := r.Dial(context.Background(), ServiceEndpoint{Name: "b", Local: false, TCPAddr: "10.0.0.2:2"}); err != nil {
		t.Fatalf("Dial remote error = %v", err)
	}
	if uds.dials != 1 || tcp.dials != 1 {
		t.Fatalf("unexpected dial counts uds=%d tcp=%d", uds.dials, tcp.dials)
	}
}

func TestRouterErrors(t *testing.T) {
	r := NewRouter(nil, nil)
	if _, err := r.Dial(context.Background(), ServiceEndpoint{Name: "svc"}); err == nil {
		t.Fatal("expected no address error")
	}
	if _, err := r.Dial(context.Background(), ServiceEndpoint{Name: "svc", UDSAddr: "/tmp/s.sock", Local: true}); err == nil {
		t.Fatal("expected nil uds transport error")
	}
	if _, err := r.Dial(context.Background(), ServiceEndpoint{Name: "svc", TCPAddr: "127.0.0.1:1"}); err == nil {
		t.Fatal("expected nil tcp transport error")
	}
	if _, err := r.Listen(context.Background(), "/tmp/s.sock"); err == nil {
		t.Fatal("expected nil uds transport error on listen")
	}
}

func TestTransportAddressValidation(t *testing.T) {
	uds := NewUDSTransport()
	if _, err := uds.Listen(context.Background(), ""); err == nil {
		t.Fatal("expected missing uds listen address error")
	}
	if _, err := uds.Dial(context.Background(), ServiceEndpoint{}); err == nil {
		t.Fatal("expected missing uds dial address error")
	}

	tcp := NewTCPTransport()
	if _, err := tcp.Dial(context.Background(), ServiceEndpoint{}); err == nil {
		t.Fatal("expected missing tcp dial address error")
	}
	if _, err := tcp.Listen(context.Background(), "not-an-address"); err == nil {
		t.Fatal("expected invalid tcp listen address error")
	}
}

func TestTCPTransportRejectsNonLoopbackByDefault(t *testing.T) {
	t.Setenv(insecureTCPListenEnv, "")
	tcp := NewTCPTransport()
	if _, err := tcp.Listen(context.Background(), "0.0.0.0:0"); err == nil {
		t.Fatal("expected non-loopback tcp listen error")
	}
}

func TestTCPTransportAllowsNonLoopbackWithOptIn(t *testing.T) {
	t.Setenv(insecureTCPListenEnv, "1")
	tcp := NewTCPTransport()
	ln, err := tcp.Listen(context.Background(), "0.0.0.0:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	_ = ln.Close()
}

func TestUDSListenRejectsExistingNonSocket(t *testing.T) {
	uds := NewUDSTransport()
	path := filepath.Join(t.TempDir(), "not-a-socket.sock")
	if err := os.WriteFile(path, []byte("not a socket"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if _, err := uds.Listen(context.Background(), path); err == nil {
		t.Fatal("expected error for existing non-socket path")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.Mode()&os.ModeSocket != 0 {
		t.Fatalf("expected regular file to remain, got mode=%v", info.Mode())
	}
}

func TestUDSListenRemovesStaleSocket(t *testing.T) {
	uds := NewUDSTransport()
	path := testSocketPath(t, "stale")

	addr, err := net.ResolveUnixAddr("unix", path)
	if err != nil {
		t.Fatalf("ResolveUnixAddr() error = %v", err)
	}
	rawLn, err := net.ListenUnix("unix", addr)
	if err != nil {
		t.Fatalf("ListenUnix() error = %v", err)
	}
	if err := rawLn.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	ln, err := uds.Listen(context.Background(), path)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	_ = ln.Close()
}

func TestListenerAcceptContextCancelled(t *testing.T) {
	ctx := context.Background()
	uds := NewUDSTransport()
	ln, err := uds.Listen(ctx, testSocketPath(t, "accept"))
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer ln.Close()

	cancelCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = ln.Accept(cancelCtx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context deadline exceeded, got %v", err)
	}
}

func TestTCPListenerAcceptContextCancelled(t *testing.T) {
	ctx := context.Background()
	tcp := NewTCPTransport()
	ln, err := tcp.Listen(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer ln.Close()

	cancelCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = ln.Accept(cancelCtx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context deadline exceeded, got %v", err)
	}
}

func TestTCPFDUnsupported(t *testing.T) {
	c := &tcpConn{}
	if err := c.SendFd(1, []byte("x")); !errors.Is(err, ErrFDUnsupported) {
		t.Fatalf("expected ErrFDUnsupported from SendFd, got %v", err)
	}
	if _, _, err := c.RecvFd(); !errors.Is(err, ErrFDUnsupported) {
		t.Fatalf("expected ErrFDUnsupported from RecvFd, got %v", err)
	}
}

func TestUDSConnFDMethods(t *testing.T) {
	c := &udsConn{}
	if err := c.SendFd(1, []byte("x")); err == nil {
		t.Fatal("expected SendFd error on nil unix conn")
	}
	if _, _, err := c.RecvFd(); err == nil {
		t.Fatal("expected RecvFd error on nil unix conn")
	}
}

type fakeTransport struct {
	dials   int
	listens int
}

func (f *fakeTransport) Dial(_ context.Context, _ ServiceEndpoint) (Conn, error) {
	f.dials++
	return &fakeConn{}, nil
}

func (f *fakeTransport) Listen(context.Context, string) (Listener, error) {
	f.listens++
	return &fakeListener{}, nil
}

type fakeListener struct{}

func (f *fakeListener) Accept(context.Context) (Conn, error) {
	return nil, errors.New("not implemented")
}

func (f *fakeListener) Close() error {
	return nil
}

func (f *fakeListener) Addr() string {
	return "fake"
}

type fakeConn struct{}

func (f *fakeConn) Send(*Message) error          { return nil }
func (f *fakeConn) Recv() (*Message, error)      { return &Message{}, nil }
func (f *fakeConn) SendFd(int, []byte) error     { return nil }
func (f *fakeConn) RecvFd() (int, []byte, error) { return -1, nil, nil }
func (f *fakeConn) SetReadDeadline(time.Time) error {
	return nil
}
func (f *fakeConn) Close() error { return nil }

func errUnexpectedMethod(got string) error { return &methodErr{got: got} }

type methodErr struct{ got string }

func (e *methodErr) Error() string { return "unexpected method: " + e.got }

// testSocketPath returns a temporary UDS socket path under /tmp.
// We intentionally use /tmp instead of t.TempDir() because Unix domain
// socket paths are limited to 108 bytes (sockaddr_un) and the paths
// generated by t.TempDir() are typically too long.
func testSocketPath(t *testing.T, prefix string) string {
	t.Helper()
	path := filepath.Join("/tmp", fmt.Sprintf("nexus-%s-%d.sock", prefix, time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.Remove(path) })
	return path
}

func TestRouterListenDelegatesToUDS(t *testing.T) {
	uds := &fakeTransport{}
	r := NewRouter(uds, nil)
	ln, err := r.Listen(context.Background(), "/tmp/unused.sock")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	if ln.Addr() != "fake" {
		t.Fatalf("unexpected listener addr: %s", ln.Addr())
	}
	if uds.listens != 1 {
		t.Fatalf("expected one uds listen call, got %d", uds.listens)
	}
}

func TestUDSTransportErrorBranches(t *testing.T) {
	uds := NewUDSTransport()
	if _, err := uds.Listen(context.Background(), "/tmp/\x00bad.sock"); err == nil {
		t.Fatal("expected invalid unix address error")
	}
	if _, err := uds.Dial(context.Background(), ServiceEndpoint{UDSAddr: "/tmp/definitely-not-exist.sock"}); err == nil {
		t.Fatal("expected dial failure for missing socket")
	}
}

func TestRouterFallbackToUDSAddress(t *testing.T) {
	uds := &fakeTransport{}
	r := NewRouter(uds, nil)
	if _, err := r.Dial(context.Background(), ServiceEndpoint{Name: "svc", UDSAddr: "/tmp/s.sock"}); err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	if uds.dials != 1 {
		t.Fatalf("expected uds dial count=1, got %d", uds.dials)
	}
}
