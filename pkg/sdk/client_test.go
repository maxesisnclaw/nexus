package sdk

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"nexus/pkg/registry"
)

func TestServeAndCall(t *testing.T) {
	reg := registry.New("node-a")
	defer reg.Close()

	sock := filepath.Join(t.TempDir(), "echo.sock")
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
