package sdk

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nexus/pkg/registry"
	"nexus/pkg/transport"
)

func TestServeAndCall(t *testing.T) {
	reg := registry.New("node-a")
	defer reg.Close()

	sock := testSocketPath(t, "echo")
	server, err := New(Config{
		Name:                  "echo",
		ID:                    "echo-1",
		Registry:              reg,
		UDSAddr:               sock,
		LargePayloadThreshold: 8,
	})
	if err != nil {
		t.Fatalf("New(server) error = %v", err)
	}
	server.Handle("echo", func(req *Request) (*Response, error) {
		return &Response{Payload: append(req.Payload, '!')}, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.Serve(ctx)
	}()
	defer server.Close()

	waitForService(t, reg, "echo", 1)

	caller, err := New(Config{Name: "caller", ID: "caller-1", Registry: reg})
	if err != nil {
		t.Fatalf("New(caller) error = %v", err)
	}
	defer caller.Close()

	resp, err := caller.Call("echo", "echo", []byte("hi"))
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if string(resp.Payload) != "hi!" {
		t.Fatalf("unexpected payload: %q", string(resp.Payload))
	}

	large := bytes.Repeat([]byte("x"), 64)
	resp, err = caller.CallWithData("echo", "echo", large)
	if err != nil {
		t.Fatalf("CallWithData() error = %v", err)
	}
	if len(resp.Payload) != len(large)+1 {
		t.Fatalf("unexpected response size: %d", len(resp.Payload))
	}

	cancel()
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("Serve() returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for Serve to stop")
	}
}

func TestNewValidationAndDefaults(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("expected New() to reject empty name")
	}
	client, err := New(Config{Name: "svc"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer client.Close()
	if client.cfg.ID != "svc" {
		t.Fatalf("expected default ID=Name, got %s", client.cfg.ID)
	}
	if client.cfg.RequestTimeout <= 0 || client.cfg.LargePayloadThreshold <= 0 || client.cfg.RetryBackoff <= 0 {
		t.Fatalf("expected positive defaults, got timeout=%s threshold=%d backoff=%s", client.cfg.RequestTimeout, client.cfg.LargePayloadThreshold, client.cfg.RetryBackoff)
	}
}

func TestCallRetrySucceedsAfterDialFailures(t *testing.T) {
	reg := registry.New("node-a")
	defer reg.Close()

	reg.Register(registry.ServiceInstance{
		Name:      "echo",
		ID:        "echo-1",
		Endpoints: []registry.Endpoint{{Type: registry.EndpointUDS, Addr: "/tmp/does-not-matter.sock"}},
	})

	ft := &flakyTransport{failuresLeft: 2}
	router := transport.NewRouter(ft, ft)
	client, err := New(Config{
		Name:         "caller",
		ID:           "caller-1",
		Registry:     reg,
		Router:       router,
		CallRetries:  2,
		RetryBackoff: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer client.Close()

	resp, err := client.Call("echo", "ping", []byte("hello"))
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if string(resp.Payload) != "hello" {
		t.Fatalf("unexpected payload: %q", string(resp.Payload))
	}
	if got := ft.dials.Load(); got != 3 {
		t.Fatalf("expected 3 dial attempts, got %d", got)
	}
}

func TestCallRetryExhausted(t *testing.T) {
	reg := registry.New("node-a")
	defer reg.Close()

	reg.Register(registry.ServiceInstance{
		Name:      "echo",
		ID:        "echo-1",
		Endpoints: []registry.Endpoint{{Type: registry.EndpointUDS, Addr: "/tmp/does-not-matter.sock"}},
	})
	ft := &flakyTransport{failuresLeft: 5}
	client, err := New(Config{
		Name:         "caller",
		ID:           "caller-1",
		Registry:     reg,
		Router:       transport.NewRouter(ft, ft),
		CallRetries:  1,
		RetryBackoff: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer client.Close()

	if _, err := client.Call("echo", "ping", []byte("hello")); err == nil {
		t.Fatal("expected dial retry failure")
	}
}

func TestCallWithDataFallbackToRegularCall(t *testing.T) {
	reg := registry.New("node-a")
	defer reg.Close()

	sock := testSocketPath(t, "fd-fallback")
	server, err := New(Config{
		Name:                  "echo",
		ID:                    "echo-1",
		Registry:              reg,
		UDSAddr:               sock,
		LargePayloadThreshold: 8,
	})
	if err != nil {
		t.Fatalf("New(server) error = %v", err)
	}
	server.Handle("echo", func(req *Request) (*Response, error) {
		return &Response{Payload: req.Payload}, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = server.Serve(ctx) }()
	defer server.Close()
	waitForService(t, reg, "echo", 1)

	caller, err := New(Config{Name: "caller", ID: "caller-1", Registry: reg, LargePayloadThreshold: 4})
	if err != nil {
		t.Fatalf("New(caller) error = %v", err)
	}
	defer caller.Close()

	payload := bytes.Repeat([]byte("x"), 32)
	resp, err := caller.CallWithData("echo", "echo", payload)
	if err != nil {
		t.Fatalf("CallWithData() error = %v", err)
	}
	if !bytes.Equal(resp.Payload, payload) {
		t.Fatalf("payload mismatch: got=%d want=%d", len(resp.Payload), len(payload))
	}
}

func TestServeValidation(t *testing.T) {
	client, err := New(Config{Name: "svc"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer client.Close()
	err = client.Serve(context.Background())
	if err == nil {
		t.Fatal("expected Serve() error without listen address")
	}
}

func TestEndpointFromInstanceErrors(t *testing.T) {
	if _, err := endpointFromInstance(registry.ServiceInstance{Name: "svc", ID: "a"}); err == nil {
		t.Fatal("expected endpointFromInstance() error without endpoints")
	}
	ep, err := endpointFromInstance(registry.ServiceInstance{
		Name: "svc",
		ID:   "a",
		Endpoints: []registry.Endpoint{
			{Type: registry.EndpointTCP, Addr: "127.0.0.1:9000"},
			{Type: registry.EndpointUDS, Addr: "/run/nexus/a.sock"},
		},
	})
	if err != nil {
		t.Fatalf("endpointFromInstance() error = %v", err)
	}
	if ep.UDSAddr == "" || ep.TCPAddr == "" || !ep.Local {
		t.Fatalf("unexpected endpoint conversion: %+v", ep)
	}
}

func TestDispatchHandlerNotFound(t *testing.T) {
	client, err := New(Config{Name: "svc", UDSAddr: filepath.Join(t.TempDir(), "svc.sock")})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer client.Close()
	if _, err := client.dispatch(&Request{Method: "missing"}); err == nil {
		t.Fatal("expected missing handler error")
	}
}

func TestCallMsgpackErrorBranches(t *testing.T) {
	client, err := New(Config{Name: "svc", UDSAddr: testSocketPath(t, "unused")})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer client.Close()

	if _, err := client.callMsgpack(&scriptedConn{
		sendErr: errors.New("send failed"),
	}, "echo", []byte("x")); err == nil {
		t.Fatal("expected send error")
	}

	if _, err := client.callMsgpack(&scriptedConn{
		recvErr: io.EOF,
	}, "echo", []byte("x")); err == nil {
		t.Fatal("expected recv error")
	}

	if _, err := client.callMsgpack(&scriptedConn{
		recvMsg: &transport.Message{Headers: map[string]string{"error": "remote error"}},
	}, "echo", []byte("x")); err == nil {
		t.Fatal("expected remote error header")
	}
}

func TestServeConnErrorHandling(t *testing.T) {
	client, err := New(Config{Name: "svc", UDSAddr: testSocketPath(t, "serve-conn")})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer client.Close()
	client.Handle("ok", func(req *Request) (*Response, error) {
		return &Response{Payload: req.Payload}, nil
	})
	client.Handle("fail", func(*Request) (*Response, error) {
		return nil, errors.New("handler failed")
	})

	conn := &scriptedConn{
		recvQueue: []*transport.Message{
			{Method: "missing"},
			{Method: "fail"},
			{Method: "ok", Payload: []byte("v")},
			{Method: fdCallMethod, Headers: map[string]string{"method": "ok"}},
			{Method: fdCallMethod, Headers: map[string]string{"method": "ok"}},
		},
		recvFdErr: errors.New("fd receive failed"),
		recvFd:    -1,
	}
	client.serveConn(conn)

	if len(conn.sent) != 7 {
		t.Fatalf("unexpected sent message count: %d", len(conn.sent))
	}
	if conn.sent[0].Headers["error"] == "" {
		t.Fatalf("expected missing handler error, got %+v", conn.sent[0])
	}
	if conn.sent[1].Headers["error"] == "" {
		t.Fatalf("expected handler failure error, got %+v", conn.sent[1])
	}
	if string(conn.sent[2].Payload) != "v" {
		t.Fatalf("unexpected success payload: %q", string(conn.sent[2].Payload))
	}
	if conn.sent[3].Headers[fdReadyKey] != "1" {
		t.Fatalf("expected fd ready ack, got %+v", conn.sent[3])
	}
	if conn.sent[4].Headers["error"] == "" {
		t.Fatalf("expected recv fd error response, got %+v", conn.sent[4])
	}
	if conn.sent[5].Headers[fdReadyKey] != "1" {
		t.Fatalf("expected second fd ready ack, got %+v", conn.sent[5])
	}
	if conn.sent[6].Headers["error"] == "" {
		t.Fatalf("expected read fd error response, got %+v", conn.sent[6])
	}
}

func TestListenTCPAndDualRegister(t *testing.T) {
	reg := registry.New("node-a")
	defer reg.Close()
	client, err := New(Config{
		Name:     "svc",
		ID:       "svc-1",
		Registry: reg,
		TCPAddr:  "127.0.0.1:0",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer client.Close()

	ln, ep, err := client.listen(context.Background())
	if err != nil {
		t.Fatalf("listen() error = %v", err)
	}
	_ = ln.Close()
	if ep.Type != registry.EndpointTCP || ep.Addr == "" {
		t.Fatalf("unexpected tcp endpoint: %+v", ep)
	}

	dual, err := New(Config{
		Name:     "svc",
		ID:       "svc-dual",
		Registry: reg,
		Network:  "dual",
		UDSAddr:  "/run/nexus/svc.sock",
		TCPAddr:  "127.0.0.1:9000",
	})
	if err != nil {
		t.Fatalf("New(dual) error = %v", err)
	}
	defer dual.Close()
	dual.register(registry.Endpoint{Type: registry.EndpointUDS, Addr: dual.cfg.UDSAddr})
	items := reg.Lookup("svc")
	if len(items) == 0 {
		t.Fatal("expected registered dual endpoint service")
	}
	var found bool
	for _, it := range items {
		if it.ID == "svc-dual" && len(it.Endpoints) == 2 {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected dual endpoint registration, got %+v", items)
	}
}

func waitForService(t *testing.T, reg *registry.Registry, name string, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := len(reg.Lookup(name)); got == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("service %s did not reach %d instances", name, want)
}

func testSocketPath(t *testing.T, prefix string) string {
	t.Helper()
	path := filepath.Join("/tmp", fmt.Sprintf("nexus-sdk-%s-%d.sock", prefix, time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.Remove(path) })
	return path
}

type flakyTransport struct {
	failuresLeft int32
	dials        atomic.Int32
	mu           sync.Mutex
}

type scriptedConn struct {
	recvQueue []*transport.Message
	recvMsg   *transport.Message
	recvErr   error
	sendErr   error
	recvFd    int
	recvFdErr error
	sent      []*transport.Message
}

func (s *scriptedConn) Send(msg *transport.Message) error {
	if s.sendErr != nil {
		return s.sendErr
	}
	cp := *msg
	s.sent = append(s.sent, &cp)
	return nil
}

func (s *scriptedConn) Recv() (*transport.Message, error) {
	if len(s.recvQueue) > 0 {
		msg := s.recvQueue[0]
		s.recvQueue = s.recvQueue[1:]
		return msg, nil
	}
	if s.recvErr != nil {
		return nil, s.recvErr
	}
	if s.recvMsg != nil {
		return s.recvMsg, nil
	}
	return nil, io.EOF
}

func (s *scriptedConn) SendFd(int, []byte) error {
	return transport.ErrFDUnsupported
}

func (s *scriptedConn) RecvFd() (int, []byte, error) {
	if s.recvFdErr != nil {
		err := s.recvFdErr
		s.recvFdErr = nil
		return -1, nil, err
	}
	if s.recvFd != 0 {
		return s.recvFd, []byte("fd"), nil
	}
	return -1, nil, transport.ErrFDUnsupported
}

func (s *scriptedConn) Close() error {
	return nil
}

func (f *flakyTransport) Dial(_ context.Context, _ transport.ServiceEndpoint) (transport.Conn, error) {
	f.dials.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failuresLeft > 0 {
		f.failuresLeft--
		return nil, errors.New("forced dial failure")
	}
	return &echoConn{}, nil
}

func (f *flakyTransport) Listen(context.Context, string) (transport.Listener, error) {
	return nil, errors.New("not implemented")
}

type echoConn struct {
	req *transport.Message
}

func (e *echoConn) Send(msg *transport.Message) error {
	e.req = msg
	return nil
}

func (e *echoConn) Recv() (*transport.Message, error) {
	if e.req == nil {
		return nil, errors.New("request missing")
	}
	return &transport.Message{Method: e.req.Method, Payload: e.req.Payload, Headers: map[string]string{}}, nil
}

func (e *echoConn) SendFd(int, []byte) error {
	return transport.ErrFDUnsupported
}

func (e *echoConn) RecvFd() (int, []byte, error) {
	return -1, nil, transport.ErrFDUnsupported
}

func (e *echoConn) Close() error {
	return nil
}
