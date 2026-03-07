//go:build integration && linux

package sdk

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/maxesisn/nexus/pkg/registry"
)

func TestIntegrationServeAndCallWithMemfd(t *testing.T) {
	reg := registry.New("integration-node")
	defer reg.Close()

	sock := filepath.Join("/tmp", fmt.Sprintf("nexus-it-sdk-%d.sock", time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.Remove(sock) })

	server, err := New(Config{
		Name:                  "processor",
		ID:                    "processor-1",
		Registry:              reg,
		UDSAddr:               sock,
		LargePayloadThreshold: 64,
	})
	if err != nil {
		t.Fatalf("New(server) error = %v", err)
	}
	server.Handle("echo", func(req *Request) (*Response, error) {
		return &Response{Payload: req.Payload}, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.Serve(ctx)
	}()
	defer server.Close()
	waitForService(t, reg, "processor", 1)

	client, err := New(Config{
		Name:                  "caller",
		ID:                    "caller-1",
		Registry:              reg,
		LargePayloadThreshold: 64,
		CallRetries:           1,
		RetryBackoff:          10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New(client) error = %v", err)
	}
	defer client.Close()

	payload := bytes.Repeat([]byte("z"), 1<<20)
	resp, err := client.CallWithData("processor", "echo", payload)
	if err != nil {
		t.Fatalf("CallWithData() error = %v", err)
	}
	if !bytes.Equal(resp.Payload, payload) {
		t.Fatalf("payload mismatch: got=%d want=%d", len(resp.Payload), len(payload))
	}

	cancel()
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("Serve() returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for server shutdown")
	}
}
