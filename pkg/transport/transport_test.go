package transport

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestUDSTransportSendRecv(t *testing.T) {
	ctx := context.Background()
	uds := NewUDSTransport()
	sock := filepath.Join(t.TempDir(), "echo.sock")
	ln, err := uds.Listen(ctx, sock)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer ln.Close()

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

type fakeTransport struct {
	dials int
}

func (f *fakeTransport) Dial(_ context.Context, _ ServiceEndpoint) (Conn, error) {
	f.dials++
	return &fakeConn{}, nil
}

func (f *fakeTransport) Listen(context.Context, string) (Listener, error) {
	return nil, nil
}

type fakeConn struct{}

func (f *fakeConn) Send(*Message) error          { return nil }
func (f *fakeConn) Recv() (*Message, error)      { return &Message{}, nil }
func (f *fakeConn) SendFd(int, []byte) error     { return nil }
func (f *fakeConn) RecvFd() (int, []byte, error) { return -1, nil, nil }
func (f *fakeConn) Close() error                 { return nil }

func errUnexpectedMethod(got string) error { return &methodErr{got: got} }

type methodErr struct{ got string }

func (e *methodErr) Error() string { return "unexpected method: " + e.got }
