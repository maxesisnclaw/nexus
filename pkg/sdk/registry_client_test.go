package sdk

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/maxesisn/nexus/pkg/config"
	"github.com/maxesisn/nexus/pkg/daemon"
)

func TestNewUsesRemoteRegistryWhenRegistryAddrSet(t *testing.T) {
	socket, stop := startControlDaemonForSDK(t)
	defer stop()

	node, err := New(Config{Name: "svc", RegistryAddr: socket})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer node.Close()

	if node.registry != nil {
		t.Fatal("expected local registry to be nil when RegistryAddr is used")
	}
	if _, ok := node.regAPI.(*registryClient); !ok {
		t.Fatalf("expected remote registry client backend, got %T", node.regAPI)
	}
}

func TestServeAndCallViaRemoteRegistry(t *testing.T) {
	socket, stop := startControlDaemonForSDK(t)
	defer stop()

	server, err := New(Config{
		Name:         "echo",
		ID:           "echo-1",
		RegistryAddr: socket,
		UDSAddr:      testSocketPath(t, "remote-registry-echo"),
	})
	if err != nil {
		t.Fatalf("New(server) error = %v", err)
	}
	defer server.Close()
	server.Handle("echo", func(req *Request) (*Response, error) {
		return &Response{Payload: append(req.Payload, '!')}, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.Serve(ctx)
	}()

	caller, err := New(Config{
		Name:         "caller",
		ID:           "caller-1",
		RegistryAddr: socket,
		CallRetries:  5,
		RetryBackoff: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New(caller) error = %v", err)
	}
	defer caller.Close()

	deadline := time.Now().Add(2 * time.Second)
	for {
		resp, callErr := caller.Call("echo", "echo", []byte("hi"))
		if callErr == nil {
			if string(resp.Payload) != "hi!" {
				t.Fatalf("unexpected payload: %q", string(resp.Payload))
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Call() did not succeed via remote registry: %v", callErr)
		}
		time.Sleep(30 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("Serve() returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for Serve() shutdown")
	}
}

func TestRegistryClientRequestDeadline(t *testing.T) {
	socket := filepath.Join("/tmp", fmt.Sprintf("nexus-sdk-registry-deadline-%d.sock", time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.Remove(socket) })
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	defer listener.Close()

	accepted := make(chan struct{})
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		close(accepted)
		defer conn.Close()
		time.Sleep(200 * time.Millisecond)
	}()

	oldDeadline := registryClientIODeadline
	registryClientIODeadline = 30 * time.Millisecond
	defer func() {
		registryClientIODeadline = oldDeadline
	}()

	client := newRegistryClient(socket, "node-a", slog.New(slog.NewJSONHandler(io.Discard, nil)))
	defer client.Close()

	start := time.Now()
	ok := client.Heartbeat("svc-1")
	elapsed := time.Since(start)
	if ok {
		t.Fatal("expected heartbeat to fail when server does not reply")
	}
	select {
	case <-accepted:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("server did not accept registry client connection")
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("expected request to fail fast due to I/O deadline, elapsed=%s", elapsed)
	}
}

func startControlDaemonForSDK(t *testing.T) (string, func()) {
	t.Helper()
	socket := filepath.Join("/tmp", fmt.Sprintf("nexus-sdk-control-%d.sock", time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.Remove(socket) })
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	d, err := daemon.New(&config.Config{
		Daemon: config.DaemonConfig{
			Socket:         socket,
			HealthInterval: config.Duration{Duration: 100 * time.Millisecond},
			ShutdownGrace:  config.Duration{Duration: 500 * time.Millisecond},
		},
	}, logger)
	if err != nil {
		t.Fatalf("daemon.New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := d.Start(ctx); err != nil {
		cancel()
		t.Fatalf("daemon.Start() error = %v", err)
	}
	return socket, func() {
		cancel()
		if err := d.Stop(); err != nil {
			t.Fatalf("daemon.Stop() error = %v", err)
		}
	}
}
