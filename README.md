# Nexus

Nexus is a lightweight microservice foundation focused on local-process orchestration, service registry/discovery, and high-performance IPC.

- Language: Go
- Supported platform: Linux (kernel >= 4.19 for full feature set)
- IPC protocol: msgpack over UDS/TCP
- Large payload path: memfd + `SCM_RIGHTS` (Linux)

## Architecture

```text
+--------------------------------------------------------------+
|                           nexusd                             |
|  +-----------------+  +-------------------+  +------------+  |
|  | Process Manager |  | Service Registry  |  |  Health    |  |
|  | start/stop/retry|  | lookup/watch/sync |  | monitoring |  |
|  +-----------------+  +-------------------+  +------------+  |
+----------------------------+---------------------------------+
                             |
         +-------------------+-------------------+
         |                                       |
   +-----v----------------+              +-------v--------------+
   | local services       |              | remote services      |
   | UDS + msgpack        |              | TCP + msgpack        |
   | optional memfd fd    |              | standard payload     |
   +----------------------+              +----------------------+
```

## Quick Start

1. Build daemon:

```bash
CGO_ENABLED=0 go build ./cmd/nexusd
```

2. Start daemon:

```bash
./nexusd -config ./nexus.toml
```

3. Run tests:

```bash
go test ./...
go test -race ./...
```

4. Run Linux integration tests inside Docker:

```bash
docker build -t nexus-test -f Dockerfile.test .
docker run --rm -v $(pwd):/workspace -w /workspace nexus-test go test -v -race ./...
docker run --rm --privileged -v $(pwd):/workspace -w /workspace nexus-test go test -v -tags integration ./...
```

## Installation

### From Source

```bash
git clone https://github.com/example/nexus.git
cd nexus
CGO_ENABLED=0 go build ./...
```

### Docker Image

```bash
docker build -t nexusd:local .
```

## Configuration Reference

Nexus uses TOML.

```toml
[daemon]
socket = "/run/nexus/registry.sock"
log_level = "info"
health_interval = "5s"
shutdown_grace = "10s"
listen = "0.0.0.0:7700"

[[daemon.peers]]
addr = "192.168.1.100:7700"

[[service]]
name = "detector"
type = "worker"
runtime = "binary"
binary = "/opt/app/detector"
args = ["--model", "vehicle"]
network = "dual"
instances = [
  { id = "detector-1", args = ["--shard", "a"] },
  { id = "detector-2", args = ["--shard", "b"] },
]

[[service]]
name = "legacy-postproc"
type = "singleton"
runtime = "docker"
image = "app/postproc:latest"
volumes = ["/opt/app/postproc:/app"]
network = "uds"
```

`depends_on` and `health_check` are reserved configuration fields for future dependency/liveness orchestration and are not enforced by the current daemon.

## SDK Examples

### Go SDK

```go
reg := registry.New("node-a")
client, _ := sdk.New(sdk.Config{Name: "echo", ID: "echo-1", Registry: reg, UDSAddr: "/tmp/echo.sock"})
client.Handle("echo", func(req *sdk.Request) (*sdk.Response, error) {
    return &sdk.Response{Payload: req.Payload}, nil
})
```

### Python SDK

Python SDK support is planned but not available yet.

See runnable programs in [`examples/`](examples/README.md).

## Development

Use the provided `Makefile`:

```bash
make build
make test
make race
make docker-test
make docker-integration
```

## Contributing

1. Fork and create a feature branch.
2. Add tests for all behavior changes.
3. Run `make vet test race` and Linux Docker integration tests.
4. Keep APIs documented and backwards compatible.
5. Submit a focused pull request with clear commit history.

## Changelog

Project history is tracked in [CHANGELOG.md](CHANGELOG.md).

## License

MIT. See [LICENSE](LICENSE).
