# Nexus

Nexus is a lightweight microservice foundation for Linux, focused on process orchestration, service registry/discovery, and high-performance IPC.

## Features (v0.4)

- Daemon-managed service lifecycle (`singleton` / `worker`)
- UDS control plane (`daemon.socket`) with msgpack protocol
- Dependency-aware startup ordering with `depends_on` (topological sort)
- Probe-based health checks (`exec://`, `http://`, `tcp://`) with PID fallback
- Transport abstraction: UDS/TCP + msgpack
- Noise-encrypted TCP transport (NK handshake)
- memfd + `SCM_RIGHTS` for large local payload transfer
- Go SDK with `Node`, `HandleFunc`, and remote registry support (`RegistryAddr`)
- CLI tools: `nexusd validate`, `nexusd status`, `nexusd keygen`

## Requirements

- Go
- Linux (kernel >= 4.19 for full feature set)

## Quick Start

1. Build daemon:

```bash
CGO_ENABLED=0 go build ./cmd/nexusd
```

2. Start daemon:

```bash
./nexusd -config ./nexus.toml
```

3. Minimal Go SDK node example (`Node`, not `Client`):

```go
package main

import (
	"github.com/maxesisn/nexus/pkg/sdk"
)

func main() {
	node, err := sdk.New(sdk.Config{
		Name:         "echo",
		ID:           "echo-1",
		UDSAddr:      "/run/nexus/echo.sock",
		RegistryAddr: "/run/nexus/registry.sock",
	})
	if err != nil {
		panic(err)
	}
	defer node.Close()

	node.HandleFunc("echo", func(req *sdk.Request) (*sdk.Response, error) {
		return &sdk.Response{Payload: req.Payload}, nil
	})

	if err := node.Serve(); err != nil {
		panic(err)
	}
}
```

4. Run checks:

```bash
go build ./...
go vet ./...
go test ./...
```

## Configuration Reference

- Full configuration and protocol design: [DESIGN.md](DESIGN.md)
- Ready-to-edit example: [`nexus.example.toml`](nexus.example.toml)

## CLI Usage

Validate config:

```bash
nexusd validate -config ./nexus.toml
```

Query daemon status:

```bash
nexusd status -socket /run/nexus/registry.sock
```

Generate Noise keypair:

```bash
nexusd keygen -out ./nexus.key
```

## Security

Nexus v0.4 uses two trust models:

- UDS trust model: access control by filesystem permissions on `/run/nexus/*` sockets.
- TCP trust model: Noise Protocol encryption/authentication (NK pattern, Curve25519 + ChaCha20-Poly1305).

For TCP listeners, configure:
- `daemon.listen`
- `daemon.noise_key_file`
- optional `daemon.trusted_keys` for strict peer key allow-listing

## Examples

Runnable examples are available in [`examples/`](examples/README.md).

## Changelog

Project history is tracked in [CHANGELOG.md](CHANGELOG.md).

## License

MIT. See [LICENSE](LICENSE).
