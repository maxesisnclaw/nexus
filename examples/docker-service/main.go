package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"nexus/pkg/registry"
	"nexus/pkg/sdk"
)

func main() {
	sock := os.Getenv("NEXUS_SERVICE_SOCKET")
	if sock == "" {
		sock = "/run/nexus/examples/docker-service.sock"
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, sock); err != nil {
		panic(err)
	}
}

func run(ctx context.Context, sock string) error {
	reg := registry.New("docker-example")
	defer reg.Close()

	client, err := sdk.New(sdk.Config{
		Name:     "docker-service",
		ID:       "docker-service-1",
		Registry: reg,
		UDSAddr:  sock,
	})
	if err != nil {
		return err
	}
	defer client.Close()

	client.Handle("process", func(req *sdk.Request) (*sdk.Response, error) {
		out := strings.ToUpper(string(req.Payload))
		return &sdk.Response{Payload: []byte(out)}, nil
	})

	fmt.Printf("docker service listening on %s\n", sock)
	return client.Serve(ctx)
}
