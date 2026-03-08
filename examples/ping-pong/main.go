package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/maxesisn/nexus/pkg/registry"
	"github.com/maxesisn/nexus/pkg/sdk"
)

func main() {
	reg := registry.New("example-ping-pong")
	defer reg.Close()

	sock := filepath.Join(os.TempDir(), "nexus-example-ping-pong.sock")
	_ = os.Remove(sock)
	defer os.Remove(sock)

	server, err := sdk.New(sdk.Config{
		Name:                  "ping",
		ID:                    "ping-1",
		Registry:              reg,
		UDSAddr:               sock,
		LargePayloadThreshold: 32,
	})
	if err != nil {
		panic(err)
	}
	defer server.Close()

	server.Handle("ping", func(req *sdk.Request) (*sdk.Response, error) {
		return &sdk.Response{Payload: []byte("pong:" + string(req.Payload))}, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = server.Serve(ctx) }()
	waitForService(reg, "ping", 1)

	caller, err := sdk.New(sdk.Config{Name: "caller", ID: "caller-1", Registry: reg})
	if err != nil {
		panic(err)
	}
	defer caller.Close()

	resp, err := caller.Call("ping", "ping", []byte("hello"))
	if err != nil {
		panic(err)
	}
	fmt.Printf("ping-pong response: %s\n", string(resp.Payload))
}

func waitForService(reg *registry.Registry, name string, want int) {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := len(reg.Lookup(name)); got == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	panic("service startup timed out")
}
