package sdk

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/maxesisn/nexus/pkg/registry"
	"github.com/maxesisn/nexus/pkg/transport"
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
	if client.cfg.RequestTimeout <= 0 || client.cfg.ServeTimeout <= 0 || client.cfg.LargePayloadThreshold <= 0 || client.cfg.RetryBackoff <= 0 {
		t.Fatalf(
			"expected positive defaults, got request_timeout=%s serve_timeout=%s threshold=%d backoff=%s",
			client.cfg.RequestTimeout,
			client.cfg.ServeTimeout,
			client.cfg.LargePayloadThreshold,
			client.cfg.RetryBackoff,
		)
	}
	if client.cfg.RegisterRetries != 3 {
		t.Fatalf("expected default RegisterRetries=3, got %d", client.cfg.RegisterRetries)
	}
}

func TestRoundRobinDiscoveryPickCleansOffsetOnMissingService(t *testing.T) {
	reg := registry.New("node-a")
	defer reg.Close()

	discovery := newRoundRobinDiscovery(&localRegistryBackend{reg: reg})
	discovery.mu.Lock()
	discovery.offset["missing"] = 7
	discovery.mu.Unlock()

	if _, err := discovery.Pick("missing"); err == nil {
		t.Fatal("expected Pick() to fail for missing service")
	}

	discovery.mu.Lock()
	_, ok := discovery.offset["missing"]
	discovery.mu.Unlock()
	if ok {
		t.Fatal("expected stale offset entry to be removed after missing lookup")
	}
}

func TestCallRetrySucceedsAfterDialFailures(t *testing.T) {
	reg := registry.New("node-a")
	defer reg.Close()

	_ = reg.Register(registry.ServiceInstance{
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

	_ = reg.Register(registry.ServiceInstance{
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

func TestCallReusesConnectionAcrossCalls(t *testing.T) {
	reg := registry.New("node-a")
	defer reg.Close()

	const addr = "/tmp/echo-reuse.sock"
	_ = reg.Register(registry.ServiceInstance{
		Name:      "echo",
		ID:        "echo-1",
		Endpoints: []registry.Endpoint{{Type: registry.EndpointUDS, Addr: addr}},
	})
	rt := &routingTransport{
		queues: map[string][]transport.Conn{
			addr: {&echoConn{}},
		},
	}
	client, err := New(Config{
		Name:     "caller",
		ID:       "caller-1",
		Registry: reg,
		Router:   transport.NewRouter(rt, rt),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer client.Close()

	resp, err := client.Call("echo", "ping", []byte("first"))
	if err != nil {
		t.Fatalf("first Call() error = %v", err)
	}
	if string(resp.Payload) != "first" {
		t.Fatalf("unexpected first payload: %q", string(resp.Payload))
	}

	resp, err = client.Call("echo", "ping", []byte("second"))
	if err != nil {
		t.Fatalf("second Call() error = %v", err)
	}
	if string(resp.Payload) != "second" {
		t.Fatalf("unexpected second payload: %q", string(resp.Payload))
	}

	if got := rt.dialCount(addr); got != 1 {
		t.Fatalf("expected one dial for reused connection, got %d", got)
	}
}

func TestCallDiscardsBadConnectionAndRedials(t *testing.T) {
	reg := registry.New("node-a")
	defer reg.Close()

	const addr = "/tmp/echo-redial.sock"
	_ = reg.Register(registry.ServiceInstance{
		Name:      "echo",
		ID:        "echo-1",
		Endpoints: []registry.Endpoint{{Type: registry.EndpointUDS, Addr: addr}},
	})
	rt := &routingTransport{
		queues: map[string][]transport.Conn{
			addr: {
				&scriptedConn{sendErr: errors.New("forced send failure")},
				&echoConn{},
			},
		},
	}
	client, err := New(Config{
		Name:         "caller",
		ID:           "caller-1",
		Registry:     reg,
		Router:       transport.NewRouter(rt, rt),
		CallRetries:  1,
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
	if got := rt.dialCount(addr); got != 2 {
		t.Fatalf("expected bad connection discard + redial, got %d dials", got)
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

func TestCallWithDataFallbackDoesNotRepickInstance(t *testing.T) {
	origCreateMemfd := createMemfd
	createMemfd = func(string, []byte) (int, error) {
		return syscall.Dup(0)
	}
	defer func() {
		createMemfd = origCreateMemfd
	}()

	reg := registry.New("node-a")
	defer reg.Close()

	_ = reg.Register(registry.ServiceInstance{
		Name:      "echo",
		ID:        "echo-1",
		Endpoints: []registry.Endpoint{{Type: registry.EndpointUDS, Addr: "/tmp/echo-1.sock"}},
	})
	_ = reg.Register(registry.ServiceInstance{
		Name:      "echo",
		ID:        "echo-2",
		Endpoints: []registry.Endpoint{{Type: registry.EndpointUDS, Addr: "/tmp/echo-2.sock"}},
	})

	rt := &routingTransport{
		queues: map[string][]transport.Conn{
			"/tmp/echo-1.sock": {
				&scriptedConn{
					recvMsg: &transport.Message{Headers: map[string]string{fdReadyKey: "1"}},
				},
				&echoConn{},
			},
			"/tmp/echo-2.sock": {
				&scriptedConn{sendErr: errors.New("unexpected fallback dial on second instance")},
			},
		},
	}
	client, err := New(Config{
		Name:                  "caller",
		ID:                    "caller-1",
		Registry:              reg,
		Router:                transport.NewRouter(rt, rt),
		LargePayloadThreshold: 4,
	})
	if err != nil {
		t.Fatalf("New(caller) error = %v", err)
	}
	defer client.Close()

	payload := bytes.Repeat([]byte("x"), 32)
	resp, err := client.CallWithData("echo", "echo", payload)
	if err != nil {
		t.Fatalf("CallWithData() error = %v", err)
	}
	if !bytes.Equal(resp.Payload, payload) {
		t.Fatalf("payload mismatch: got=%d want=%d", len(resp.Payload), len(payload))
	}
	if got := rt.dialCount("/tmp/echo-1.sock"); got != 2 {
		t.Fatalf("expected fd attempt plus fallback dial on same instance, got %d", got)
	}
	if got := rt.dialCount("/tmp/echo-2.sock"); got != 0 {
		t.Fatalf("expected no fallback dial on second instance, got %d", got)
	}
}

func TestCallWithDataFallbackOnFDSetupAndAckFailures(t *testing.T) {
	origCreateMemfd := createMemfd
	createMemfd = func(string, []byte) (int, error) {
		return syscall.Dup(0)
	}
	defer func() {
		createMemfd = origCreateMemfd
	}()

	tests := []struct {
		name      string
		firstConn *scriptedConn
	}{
		{
			name:      "fd setup send error",
			firstConn: &scriptedConn{sendErr: errors.New("setup send failed")},
		},
		{
			name:      "fd ack recv error",
			firstConn: &scriptedConn{recvErr: io.EOF},
		},
		{
			name:      "fd ack not ready",
			firstConn: &scriptedConn{recvMsg: &transport.Message{Headers: map[string]string{}}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reg := registry.New("node-a")
			defer reg.Close()

			const addr = "/tmp/echo-fd-fallback.sock"
			_ = reg.Register(registry.ServiceInstance{
				Name:      "echo",
				ID:        "echo-1",
				Endpoints: []registry.Endpoint{{Type: registry.EndpointUDS, Addr: addr}},
			})

			rt := &routingTransport{
				queues: map[string][]transport.Conn{
					addr: {
						tc.firstConn,
						&echoConn{},
					},
				},
			}
			client, err := New(Config{
				Name:                  "caller",
				ID:                    "caller-1",
				Registry:              reg,
				Router:                transport.NewRouter(rt, rt),
				LargePayloadThreshold: 4,
			})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			defer client.Close()

			payload := bytes.Repeat([]byte("x"), 32)
			resp, err := client.CallWithData("echo", "echo", payload)
			if err != nil {
				t.Fatalf("CallWithData() error = %v", err)
			}
			if !bytes.Equal(resp.Payload, payload) {
				t.Fatalf("payload mismatch: got=%d want=%d", len(resp.Payload), len(payload))
			}
			if got := rt.dialCount(addr); got != 2 {
				t.Fatalf("expected fd attempt plus fallback dial, got %d", got)
			}
		})
	}
}

func TestCallWithDataFallbackAcquireFailure(t *testing.T) {
	origCreateMemfd := createMemfd
	createMemfd = func(string, []byte) (int, error) {
		return syscall.Dup(0)
	}
	defer func() {
		createMemfd = origCreateMemfd
	}()

	reg := registry.New("node-a")
	defer reg.Close()

	const addr = "/tmp/echo-fd-fallback-acquire.sock"
	_ = reg.Register(registry.ServiceInstance{
		Name:      "echo",
		ID:        "echo-1",
		Endpoints: []registry.Endpoint{{Type: registry.EndpointUDS, Addr: addr}},
	})

	rt := &routingTransport{
		queues: map[string][]transport.Conn{
			addr: {
				&scriptedConn{sendErr: errors.New("setup send failed")},
			},
		},
	}
	client, err := New(Config{
		Name:                  "caller",
		ID:                    "caller-1",
		Registry:              reg,
		Router:                transport.NewRouter(rt, rt),
		LargePayloadThreshold: 4,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer client.Close()

	payload := bytes.Repeat([]byte("x"), 32)
	if _, err := client.CallWithData("echo", "echo", payload); err == nil {
		t.Fatal("expected fallback acquire error")
	}
}

func TestCallWithDataFDResponseBranches(t *testing.T) {
	origCreateMemfd := createMemfd
	createMemfd = func(string, []byte) (int, error) {
		return syscall.Dup(0)
	}
	defer func() {
		createMemfd = origCreateMemfd
	}()

	tests := []struct {
		name      string
		conn      *scriptedConn
		wantErr   string
		wantBytes []byte
	}{
		{
			name: "fd response recv error",
			conn: &scriptedConn{
				sendFdOK: true,
				recvQueue: []*transport.Message{
					{Headers: map[string]string{fdReadyKey: "1"}},
				},
				recvErr: io.EOF,
			},
			wantErr: io.EOF.Error(),
		},
		{
			name: "fd response error header",
			conn: &scriptedConn{
				sendFdOK: true,
				recvQueue: []*transport.Message{
					{Headers: map[string]string{fdReadyKey: "1"}},
					{Headers: map[string]string{"error": "remote fd error"}},
				},
			},
			wantErr: "remote fd error",
		},
		{
			name: "fd response success",
			conn: &scriptedConn{
				sendFdOK: true,
				recvQueue: []*transport.Message{
					{Headers: map[string]string{fdReadyKey: "1"}},
					{Payload: []byte("ok"), Headers: map[string]string{"trace": "1"}},
				},
			},
			wantBytes: []byte("ok"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reg := registry.New("node-a")
			defer reg.Close()

			const addr = "/tmp/echo-fd-response.sock"
			_ = reg.Register(registry.ServiceInstance{
				Name:      "echo",
				ID:        "echo-1",
				Endpoints: []registry.Endpoint{{Type: registry.EndpointUDS, Addr: addr}},
			})

			rt := &routingTransport{
				queues: map[string][]transport.Conn{
					addr: {tc.conn},
				},
			}
			client, err := New(Config{
				Name:                  "caller",
				ID:                    "caller-1",
				Registry:              reg,
				Router:                transport.NewRouter(rt, rt),
				LargePayloadThreshold: 4,
			})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			defer client.Close()

			payload := bytes.Repeat([]byte("x"), 32)
			resp, err := client.CallWithData("echo", "echo", payload)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
				}
				if got := rt.dialCount(addr); got != 1 {
					t.Fatalf("expected no fallback dial on fd response error, got %d", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("CallWithData() error = %v", err)
			}
			if !bytes.Equal(resp.Payload, tc.wantBytes) {
				t.Fatalf("payload mismatch: got=%q want=%q", string(resp.Payload), string(tc.wantBytes))
			}
			if got := rt.dialCount(addr); got != 1 {
				t.Fatalf("expected single fd call dial, got %d", got)
			}
		})
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
	if _, err := endpointFromInstance(registry.ServiceInstance{Name: "svc", ID: "a"}, "node-a"); err == nil {
		t.Fatal("expected endpointFromInstance() error without endpoints")
	}
	ep, err := endpointFromInstance(registry.ServiceInstance{
		Name: "svc",
		ID:   "a",
		Node: "node-b",
		Endpoints: []registry.Endpoint{
			{Type: registry.EndpointTCP, Addr: "127.0.0.1:9000"},
			{Type: registry.EndpointUDS, Addr: "/run/nexus/a.sock"},
		},
	}, "node-a")
	if err != nil {
		t.Fatalf("endpointFromInstance() error = %v", err)
	}
	if ep.UDSAddr == "" || ep.TCPAddr == "" || ep.Local {
		t.Fatalf("unexpected endpoint conversion: %+v", ep)
	}

	localEP, err := endpointFromInstance(registry.ServiceInstance{
		Name: "svc",
		ID:   "b",
		Node: "node-a",
		Endpoints: []registry.Endpoint{
			{Type: registry.EndpointUDS, Addr: "/run/nexus/b.sock"},
		},
	}, "node-a")
	if err != nil {
		t.Fatalf("endpointFromInstance() local error = %v", err)
	}
	if !localEP.Local {
		t.Fatalf("expected local endpoint, got %+v", localEP)
	}
}

func TestEndpointFromInstanceParsesNoisePublicKeyMetadata(t *testing.T) {
	_, pub := transport.GenerateKeypair()
	ep, err := endpointFromInstance(registry.ServiceInstance{
		Name: "svc",
		ID:   "svc-1",
		Node: "node-b",
		Endpoints: []registry.Endpoint{
			{Type: registry.EndpointTCP, Addr: "127.0.0.1:9000"},
		},
		Metadata: map[string]string{
			tcpNoisePublicKeyMetadata: hex.EncodeToString(pub),
		},
	}, "node-a")
	if err != nil {
		t.Fatalf("endpointFromInstance() error = %v", err)
	}
	if !bytes.Equal(ep.PublicKey, pub) {
		t.Fatalf("unexpected parsed noise public key: got=%x want=%x", ep.PublicKey, pub)
	}
}

func TestDispatchHandlerNotFound(t *testing.T) {
	node, err := New(Config{Name: "svc", UDSAddr: filepath.Join(t.TempDir(), "svc.sock")})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer node.Close()
	if _, err := node.dispatch(&Request{Method: "missing"}); err == nil {
		t.Fatal("expected missing handler error")
	} else if !strings.Contains(err.Error(), `dispatch method "missing"`) {
		t.Fatalf("expected method name in dispatch error, got %v", err)
	}
}

func TestHandleFuncRegistersPlainFunction(t *testing.T) {
	node, err := New(Config{Name: "svc", UDSAddr: filepath.Join(t.TempDir(), "svc.sock")})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer node.Close()
	node.HandleFunc("echo", func(req *Request) (*Response, error) {
		return &Response{Payload: append(req.Payload, '!')}, nil
	})

	resp, err := node.dispatch(&Request{Method: "echo", Payload: []byte("ok")})
	if err != nil {
		t.Fatalf("dispatch() error = %v", err)
	}
	if string(resp.Payload) != "ok!" {
		t.Fatalf("unexpected payload: %q", string(resp.Payload))
	}
}

func TestCallIncludesNodeServiceAndMethodContext(t *testing.T) {
	node, err := New(Config{Name: "caller", ID: "caller-1"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer node.Close()

	_, err = node.Call("missing-svc", "echo", []byte("hello"))
	if err == nil {
		t.Fatal("expected call error")
	}
	msg := err.Error()
	if !strings.Contains(msg, `node caller/caller-1`) {
		t.Fatalf("expected node context, got %v", err)
	}
	if !strings.Contains(msg, `service="missing-svc"`) {
		t.Fatalf("expected service context, got %v", err)
	}
	if !strings.Contains(msg, `method="echo"`) {
		t.Fatalf("expected method context, got %v", err)
	}
}

func TestCallMsgpackErrorBranches(t *testing.T) {
	client, err := New(Config{Name: "svc", UDSAddr: testSocketPath(t, "unused")})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer client.Close()

	if _, _, err := client.callMsgpack(&scriptedConn{
		sendErr: errors.New("send failed"),
	}, "echo", []byte("x")); err == nil {
		t.Fatal("expected send error")
	}

	if _, _, err := client.callMsgpack(&scriptedConn{
		recvErr: io.EOF,
	}, "echo", []byte("x")); err == nil {
		t.Fatal("expected recv error")
	}

	if _, _, err := client.callMsgpack(&scriptedConn{
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

func TestServeConnTimeout(t *testing.T) {
	client, err := New(Config{
		Name:         "svc",
		UDSAddr:      testSocketPath(t, "serve-timeout"),
		ServeTimeout: 40 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer client.Close()

	conn := newBlockingConn()
	done := make(chan struct{})
	go func() {
		client.serveConn(conn)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("serveConn() did not return on read timeout")
	}
	if !conn.isClosed() {
		t.Fatal("expected connection to be closed after timeout")
	}
}

func TestCloseUsesSyncOnce(t *testing.T) {
	client, err := New(Config{Name: "svc", UDSAddr: testSocketPath(t, "close-once")})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	listener := &countingListener{closeErr: errors.New("close failed")}
	client.listener = listener

	err1 := client.Close()
	err2 := client.Close()
	if err1 == nil || err2 == nil {
		t.Fatalf("expected close error on both calls, got err1=%v err2=%v", err1, err2)
	}
	if listener.closeCalls != 1 {
		t.Fatalf("expected listener close once, got %d", listener.closeCalls)
	}
}

func TestCloseCleansConnectionPoolResources(t *testing.T) {
	reg := registry.New("node-a")
	defer reg.Close()

	const addr = "/tmp/echo-close-pool.sock"
	_ = reg.Register(registry.ServiceInstance{
		Name:      "echo",
		ID:        "echo-1",
		Endpoints: []registry.Endpoint{{Type: registry.EndpointUDS, Addr: addr}},
	})

	tracked := &trackingEchoConn{}
	rt := &routingTransport{
		queues: map[string][]transport.Conn{
			addr: {tracked},
		},
	}
	client, err := New(Config{
		Name:     "caller",
		ID:       "caller-1",
		Registry: reg,
		Router:   transport.NewRouter(rt, rt),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if _, err := client.Call("echo", "ping", []byte("hello")); err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if got := tracked.closeCalls.Load(); got != 0 {
		t.Fatalf("expected pooled connection to remain open before Close(), got %d close calls", got)
	}

	if err := client.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if got := tracked.closeCalls.Load(); got != 1 {
		t.Fatalf("expected Close() to clean pooled connection once, got %d", got)
	}
}

func TestCloseClosesOwnedRegistry(t *testing.T) {
	client, err := New(Config{Name: "svc-owned"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if !client.ownsReg {
		t.Fatal("expected client to own auto-created registry")
	}

	reg := client.registry
	if err := client.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	_ = reg.Register(registry.ServiceInstance{
		Name: "svc-owned",
		ID:   "owned-ephemeral",
		TTL:  20 * time.Millisecond,
	})
	time.Sleep(1200 * time.Millisecond)
	if got := len(reg.Lookup("svc-owned")); got != 1 {
		t.Fatalf("expected owned registry reaper to be stopped after Close(), got %d instances", got)
	}
}

func TestCloseDoesNotCloseExternalRegistry(t *testing.T) {
	reg := registry.NewWithOptions("node-a", 50*time.Millisecond, 10*time.Millisecond)
	defer reg.Close()

	client, err := New(Config{Name: "svc-external", Registry: reg})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if client.ownsReg {
		t.Fatal("expected client to not own external registry")
	}

	if err := client.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	_ = reg.Register(registry.ServiceInstance{
		Name: "svc-external",
		ID:   "external-ephemeral",
		TTL:  30 * time.Millisecond,
	})
	waitForServiceGone(t, reg, "svc-external")
}

func TestListenTCPAndDualRegister(t *testing.T) {
	reg := registry.New("node-a")
	defer reg.Close()
	client, err := New(Config{
		Name:     "svc",
		ID:       "svc-1",
		Registry: reg,
		Network:  "tcp",
		TCPAddr:  "127.0.0.1:0",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer client.Close()

	listeners, endpoints, err := client.listen(context.Background())
	if err != nil {
		t.Fatalf("listen() error = %v", err)
	}
	for _, ln := range listeners {
		_ = ln.Close()
	}
	if len(endpoints) != 1 || endpoints[0].Type != registry.EndpointTCP || endpoints[0].Addr == "" {
		t.Fatalf("unexpected tcp endpoint list: %+v", endpoints)
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
	if err := dual.register(context.Background(), []registry.Endpoint{
		{Type: registry.EndpointUDS, Addr: dual.cfg.UDSAddr},
		{Type: registry.EndpointTCP, Addr: dual.cfg.TCPAddr},
	}); err != nil {
		t.Fatalf("register() error = %v", err)
	}
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

func TestListenUsesBuiltInServerTransports(t *testing.T) {
	client, err := New(Config{
		Name:    "svc",
		UDSAddr: testSocketPath(t, "listen-built-in"),
		Router:  transport.NewRouter(nil, nil),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer client.Close()

	listeners, endpoints, err := client.listen(context.Background())
	if err != nil {
		t.Fatalf("listen() error = %v", err)
	}
	for _, ln := range listeners {
		_ = ln.Close()
	}
	if len(endpoints) != 1 || endpoints[0].Type != registry.EndpointUDS || endpoints[0].Addr == "" {
		t.Fatalf("unexpected uds endpoint list: %+v", endpoints)
	}
}

func TestServeDualNetworkRegistersBoundTCPAndRoutesRemoteOverTCP(t *testing.T) {
	regLocal := registry.New("node-a")
	defer regLocal.Close()

	udsPath := testSocketPath(t, "dual-serve")
	server, err := New(Config{
		Name:     "svc",
		ID:       "svc-dual",
		Registry: regLocal,
		Network:  "dual",
		UDSAddr:  udsPath,
		TCPAddr:  "127.0.0.1:0",
	})
	if err != nil {
		t.Fatalf("New(server) error = %v", err)
	}
	defer server.Close()
	server.Handle("echo", func(req *Request) (*Response, error) {
		return &Response{Payload: append(req.Payload, '!')}, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.Serve(ctx)
	}()
	defer func() {
		cancel()
		select {
		case err := <-serveErr:
			if err != nil {
				t.Fatalf("Serve() returned error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timeout waiting for Serve() shutdown")
		}
	}()

	waitForService(t, regLocal, "svc", 1)
	items := regLocal.Lookup("svc")
	if len(items) != 1 {
		t.Fatalf("expected one local service instance, got %+v", items)
	}

	var registeredUDS string
	var registeredTCP string
	for _, ep := range items[0].Endpoints {
		switch ep.Type {
		case registry.EndpointUDS:
			registeredUDS = ep.Addr
		case registry.EndpointTCP:
			registeredTCP = ep.Addr
		}
	}
	if registeredUDS != udsPath {
		t.Fatalf("unexpected registered UDS endpoint: %s", registeredUDS)
	}
	host, port, err := net.SplitHostPort(registeredTCP)
	if err != nil || host == "" || port == "" || port == "0" {
		t.Fatalf("expected bound tcp endpoint instead of wildcard, got %q (err=%v)", registeredTCP, err)
	}

	regRemote := registry.New("node-b")
	defer regRemote.Close()
	snapshot := regLocal.Snapshot()
	if len(snapshot) != 1 {
		t.Fatalf("unexpected snapshot size: %d", len(snapshot))
	}
	for i := range snapshot[0].Endpoints {
		if snapshot[0].Endpoints[i].Type == registry.EndpointUDS {
			snapshot[0].Endpoints[i].Addr = testSocketPath(t, "remote-unreachable")
		}
	}
	regRemote.MergeSnapshot(snapshot)

	remoteCaller, err := New(Config{
		Name:     "caller",
		ID:       "caller-remote",
		Registry: regRemote,
	})
	if err != nil {
		t.Fatalf("New(remote caller) error = %v", err)
	}
	defer remoteCaller.Close()

	resp, err := remoteCaller.Call("svc", "echo", []byte("ok"))
	if err != nil {
		t.Fatalf("remote Call() error = %v", err)
	}
	if string(resp.Payload) != "ok!" {
		t.Fatalf("unexpected response payload: %q", string(resp.Payload))
	}
}

func TestServeTCPNoiseRegistersMetadataAndRoutesCalls(t *testing.T) {
	reg := registry.New("node-a")
	defer reg.Close()

	noisePriv, noisePub := transport.GenerateKeypair()
	server, err := New(Config{
		Name:            "svc-noise",
		ID:              "svc-noise-1",
		Registry:        reg,
		Network:         "tcp",
		TCPAddr:         "127.0.0.1:0",
		NoisePrivateKey: noisePriv,
		NoisePublicKey:  noisePub,
	})
	if err != nil {
		t.Fatalf("New(server) error = %v", err)
	}
	defer server.Close()
	server.Handle("echo", func(req *Request) (*Response, error) {
		return &Response{Payload: append(req.Payload, '!')}, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.Serve(ctx)
	}()
	defer func() {
		cancel()
		select {
		case err := <-serveErr:
			if err != nil {
				t.Fatalf("Serve() returned error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timeout waiting for Serve() shutdown")
		}
	}()

	waitForService(t, reg, "svc-noise", 1)
	items := reg.Lookup("svc-noise")
	if len(items) != 1 {
		t.Fatalf("expected one service entry, got %+v", items)
	}
	if got := items[0].Metadata[tcpNoisePublicKeyMetadata]; got != hex.EncodeToString(noisePub) {
		t.Fatalf("unexpected registered noise public key metadata: %q", got)
	}

	caller, err := New(Config{
		Name:           "caller-noise",
		ID:             "caller-noise-1",
		Registry:       reg,
		NoisePublicKey: noisePub, // enables Noise TCP dialer selection
	})
	if err != nil {
		t.Fatalf("New(caller) error = %v", err)
	}
	defer caller.Close()

	resp, err := caller.Call("svc-noise", "echo", []byte("ok"))
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if string(resp.Payload) != "ok!" {
		t.Fatalf("unexpected response payload: %q", string(resp.Payload))
	}
}

func TestServeReturnsRegistrationErrorAfterRetries(t *testing.T) {
	node, err := New(Config{
		Name:            "svc-register-fail",
		ID:              "svc-register-fail-1",
		UDSAddr:         testSocketPath(t, "register-fail"),
		RegisterRetries: 3,
		RetryBackoff:    time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer node.Close()

	backend := &failingRegistryBackend{registerErr: errors.New("forced register failure")}
	node.regAPI = backend
	node.discovery = newRoundRobinDiscovery(backend)
	node.ownsReg = false

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err = node.Serve(ctx)
	if err == nil {
		t.Fatal("expected Serve() to fail when registration retries are exhausted")
	}
	if !strings.Contains(err.Error(), "register service instance") {
		t.Fatalf("expected registration error context, got %v", err)
	}
	if got := backend.registerCalls.Load(); got != 3 {
		t.Fatalf("expected 3 registration attempts, got %d", got)
	}
	if node.state != nodeNew {
		t.Fatalf("expected node state reset to nodeNew on registration failure, got %d", node.state)
	}
}

func TestNoiseTrustedKeysRejectUntrustedRegistryKey(t *testing.T) {
	reg := registry.New("node-a")
	defer reg.Close()

	noisePriv, noisePub := transport.GenerateKeypair()
	server, err := New(Config{
		Name:            "svc-noise-trusted",
		ID:              "svc-noise-trusted-1",
		Registry:        reg,
		Network:         "tcp",
		TCPAddr:         "127.0.0.1:0",
		NoisePrivateKey: noisePriv,
		NoisePublicKey:  noisePub,
	})
	if err != nil {
		t.Fatalf("New(server) error = %v", err)
	}
	defer server.Close()
	server.Handle("echo", func(req *Request) (*Response, error) {
		return &Response{Payload: req.Payload}, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.Serve(ctx)
	}()
	defer func() {
		cancel()
		select {
		case err := <-serveErr:
			if err != nil {
				t.Fatalf("Serve() returned error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timeout waiting for Serve() shutdown")
		}
	}()

	waitForService(t, reg, "svc-noise-trusted", 1)
	_, otherPub := transport.GenerateKeypair()
	caller, err := New(Config{
		Name:             "caller-noise-trusted",
		ID:               "caller-noise-trusted-1",
		Registry:         reg,
		NoisePublicKey:   noisePub,
		TrustedNoiseKeys: []string{hex.EncodeToString(otherPub)},
	})
	if err != nil {
		t.Fatalf("New(caller) error = %v", err)
	}
	defer caller.Close()

	if _, err := caller.Call("svc-noise-trusted", "echo", []byte("ok")); err == nil {
		t.Fatal("expected call to fail for untrusted noise key")
	} else if !strings.Contains(err.Error(), "not trusted") {
		t.Fatalf("expected untrusted key error, got %v", err)
	}
}

func TestNoiseWithoutTrustedKeysLogsWarningForRemoteTCP(t *testing.T) {
	reg := registry.New("node-a")
	defer reg.Close()

	noisePriv, noisePub := transport.GenerateKeypair()
	server, err := New(Config{
		Name:            "svc-noise-warning",
		ID:              "svc-noise-warning-1",
		Registry:        reg,
		Network:         "tcp",
		TCPAddr:         "127.0.0.1:0",
		NoisePrivateKey: noisePriv,
		NoisePublicKey:  noisePub,
	})
	if err != nil {
		t.Fatalf("New(server) error = %v", err)
	}
	defer server.Close()
	server.Handle("echo", func(req *Request) (*Response, error) {
		return &Response{Payload: append(req.Payload, '!')}, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.Serve(ctx)
	}()
	defer func() {
		cancel()
		select {
		case err := <-serveErr:
			if err != nil {
				t.Fatalf("Serve() returned error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timeout waiting for Serve() shutdown")
		}
	}()

	waitForService(t, reg, "svc-noise-warning", 1)

	var logs bytes.Buffer
	caller, err := New(Config{
		Name:           "caller-noise-warning",
		ID:             "caller-noise-warning-1",
		Registry:       reg,
		NoisePublicKey: noisePub,
		Logger:         slog.New(slog.NewJSONHandler(&logs, nil)),
	})
	if err != nil {
		t.Fatalf("New(caller) error = %v", err)
	}
	defer caller.Close()

	resp, err := caller.Call("svc-noise-warning", "echo", []byte("ok"))
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if string(resp.Payload) != "ok!" {
		t.Fatalf("unexpected response payload: %q", string(resp.Payload))
	}
	if !strings.Contains(logs.String(), "No trusted Noise keys configured; accepting any authenticated peer") {
		t.Fatalf("expected warning log for empty trusted keys, got logs: %s", logs.String())
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

func waitForServiceGone(t *testing.T, reg *registry.Registry, name string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if got := len(reg.Lookup(name)); got == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("service %s did not expire from registry", name)
}

func testSocketPath(t *testing.T, prefix string) string {
	t.Helper()
	path := filepath.Join("/tmp", fmt.Sprintf("nexus-sdk-%s-%d.sock", prefix, time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.Remove(path) })
	return path
}

type failingRegistryBackend struct {
	registerErr   error
	registerCalls atomic.Int32
}

func (b *failingRegistryBackend) Register(registry.ServiceInstance) error {
	b.registerCalls.Add(1)
	return b.registerErr
}

func (b *failingRegistryBackend) Unregister(string) {}

func (b *failingRegistryBackend) Heartbeat(string) bool {
	return false
}

func (b *failingRegistryBackend) Lookup(string) []registry.ServiceInstance {
	return nil
}

func (b *failingRegistryBackend) Watch(string, func(registry.ChangeEvent)) func() {
	return func() {}
}

func (b *failingRegistryBackend) NodeID() string {
	return "node-test"
}

func (b *failingRegistryBackend) Close() error {
	return nil
}

type flakyTransport struct {
	failuresLeft int32
	dials        atomic.Int32
	mu           sync.Mutex
}

type routingTransport struct {
	mu      sync.Mutex
	queues  map[string][]transport.Conn
	history map[string]int
}

type scriptedConn struct {
	recvQueue []*transport.Message
	recvMsg   *transport.Message
	recvErr   error
	sendErr   error
	sendFdErr error
	sendFdOK  bool
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
	if s.sendFdErr != nil {
		return s.sendFdErr
	}
	if s.sendFdOK {
		return nil
	}
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

func (s *scriptedConn) SetReadDeadline(time.Time) error {
	return nil
}

func (s *scriptedConn) SetWriteDeadline(time.Time) error {
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

func (r *routingTransport) Dial(_ context.Context, target transport.ServiceEndpoint) (transport.Conn, error) {
	addr := target.UDSAddr
	if addr == "" {
		addr = target.TCPAddr
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.history == nil {
		r.history = make(map[string]int)
	}
	r.history[addr]++
	queue := r.queues[addr]
	if len(queue) == 0 {
		return nil, fmt.Errorf("no scripted connection for %s", addr)
	}
	conn := queue[0]
	r.queues[addr] = queue[1:]
	return conn, nil
}

func (r *routingTransport) Listen(context.Context, string) (transport.Listener, error) {
	return nil, errors.New("not implemented")
}

func (r *routingTransport) dialCount(addr string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.history[addr]
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

func (e *echoConn) SetReadDeadline(time.Time) error {
	return nil
}

func (e *echoConn) SetWriteDeadline(time.Time) error {
	return nil
}

type trackingEchoConn struct {
	req        *transport.Message
	closeCalls atomic.Int32
}

func (e *trackingEchoConn) Send(msg *transport.Message) error {
	e.req = msg
	return nil
}

func (e *trackingEchoConn) Recv() (*transport.Message, error) {
	if e.req == nil {
		return nil, errors.New("request missing")
	}
	return &transport.Message{Method: e.req.Method, Payload: e.req.Payload, Headers: map[string]string{}}, nil
}

func (e *trackingEchoConn) SendFd(int, []byte) error {
	return transport.ErrFDUnsupported
}

func (e *trackingEchoConn) RecvFd() (int, []byte, error) {
	return -1, nil, transport.ErrFDUnsupported
}

func (e *trackingEchoConn) Close() error {
	e.closeCalls.Add(1)
	return nil
}

func (e *trackingEchoConn) SetReadDeadline(time.Time) error {
	return nil
}

func (e *trackingEchoConn) SetWriteDeadline(time.Time) error {
	return nil
}

type blockingConn struct {
	closed    chan struct{}
	closeOnce sync.Once
	mu        sync.Mutex
	deadline  time.Time
}

func newBlockingConn() *blockingConn {
	return &blockingConn{closed: make(chan struct{})}
}

func (b *blockingConn) Send(*transport.Message) error {
	return nil
}

func (b *blockingConn) Recv() (*transport.Message, error) {
	b.mu.Lock()
	deadline := b.deadline
	b.mu.Unlock()

	if deadline.IsZero() {
		<-b.closed
		return nil, io.EOF
	}

	wait := time.Until(deadline)
	if wait <= 0 {
		return nil, os.ErrDeadlineExceeded
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-b.closed:
		return nil, io.EOF
	case <-timer.C:
		return nil, os.ErrDeadlineExceeded
	}
}

func (b *blockingConn) SendFd(int, []byte) error {
	return transport.ErrFDUnsupported
}

func (b *blockingConn) RecvFd() (int, []byte, error) {
	return -1, nil, transport.ErrFDUnsupported
}

func (b *blockingConn) Close() error {
	b.closeOnce.Do(func() {
		close(b.closed)
	})
	return nil
}

func (b *blockingConn) isClosed() bool {
	select {
	case <-b.closed:
		return true
	default:
		return false
	}
}

func (b *blockingConn) SetReadDeadline(t time.Time) error {
	b.mu.Lock()
	b.deadline = t
	b.mu.Unlock()
	return nil
}

func (b *blockingConn) SetWriteDeadline(time.Time) error {
	return nil
}

type countingListener struct {
	closeCalls int
	closeErr   error
}

func (l *countingListener) Accept(context.Context) (transport.Conn, error) {
	return nil, errors.New("not implemented")
}

func (l *countingListener) Close() error {
	l.closeCalls++
	return l.closeErr
}

func (l *countingListener) Addr() string {
	return ""
}
