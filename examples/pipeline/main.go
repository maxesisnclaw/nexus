package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"nexus/pkg/registry"
	"nexus/pkg/sdk"
)

func main() {
	reg := registry.New("example-pipeline")
	defer reg.Close()

	normalizeSock := filepath.Join(os.TempDir(), "nexus-example-normalize.sock")
	enrichSock := filepath.Join(os.TempDir(), "nexus-example-enrich.sock")
	_ = os.Remove(normalizeSock)
	_ = os.Remove(enrichSock)
	defer os.Remove(normalizeSock)
	defer os.Remove(enrichSock)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	normalizer, err := sdk.New(sdk.Config{Name: "normalize", ID: "normalize-1", Registry: reg, UDSAddr: normalizeSock})
	if err != nil {
		panic(err)
	}
	defer normalizer.Close()
	normalizer.Handle("normalize", func(req *sdk.Request) (*sdk.Response, error) {
		norm := strings.ToLower(strings.TrimSpace(string(req.Payload)))
		return &sdk.Response{Payload: []byte(norm)}, nil
	})
	go func() { _ = normalizer.Serve(ctx) }()

	enricher, err := sdk.New(sdk.Config{Name: "enrich", ID: "enrich-1", Registry: reg, UDSAddr: enrichSock})
	if err != nil {
		panic(err)
	}
	defer enricher.Close()
	enricher.Handle("decorate", func(req *sdk.Request) (*sdk.Response, error) {
		out := "[pipeline] " + string(req.Payload)
		return &sdk.Response{Payload: []byte(out)}, nil
	})
	go func() { _ = enricher.Serve(ctx) }()

	waitForService(reg, "normalize", 1)
	waitForService(reg, "enrich", 1)

	caller, err := sdk.New(sdk.Config{Name: "pipeline-client", ID: "pipeline-client-1", Registry: reg})
	if err != nil {
		panic(err)
	}
	defer caller.Close()

	stage1, err := caller.Call("normalize", "normalize", []byte("  Nexus Pipeline  "))
	if err != nil {
		panic(err)
	}
	stage2, err := caller.Call("enrich", "decorate", stage1.Payload)
	if err != nil {
		panic(err)
	}

	fmt.Printf("pipeline result: %s\n", string(stage2.Payload))
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
