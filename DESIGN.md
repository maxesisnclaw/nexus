# Nexus — Microservice Foundation

## Overview

Nexus is a Linux-focused microservice foundation for local process orchestration, service registry/discovery, and high-performance IPC.

v0.4 adds:
- Daemon control plane over UDS (`daemon.socket`)
- Dependency-aware startup ordering (`depends_on`)
- Probe-based health checks (`health_check`)
- Noise-encrypted TCP transport
- CLI subcommands for validation/status/key generation
- Go SDK API refinement (`Client` -> `Node`)

## Architecture

```text
+--------------------------------------------------------------------------------+
|                                   nexusd                                       |
|  +------------------+  +------------------+  +------------------------------+  |
|  | Process Manager  |  | Service Registry |  | Health Monitor               |  |
|  | start/stop/retry |  | reg/lookup/watch |  | pid + probe checks           |  |
|  +------------------+  +------------------+  +------------------------------+  |
|  +-------------------------------------------------------------------------+   |
|  | Control Plane (daemon.socket): status/health/register/lookup/watch...   |   |
|  +-------------------------------------------------------------------------+   |
+-------------------------------+-----------------------------------------------+
                                |
              +-----------------+-----------------+
              |                                   |
        Local services                        Remote services
        UDS + msgpack                         TCP + Noise + msgpack
        optional memfd fd                     encrypted transport
```

## Configuration

Nexus uses TOML configuration.

### Daemon Fields

```toml
[daemon]
socket = "/run/nexus/registry.sock"  # default
log_level = "info"                    # default: debug|info|warn|error
health_interval = "5s"                # default
shutdown_grace = "10s"                # default
listen = "127.0.0.1:7700"             # optional Noise TCP listener
noise_key_file = "/etc/nexus/noise.key"
trusted_keys = ["<hex-public-key>"]    # optional trusted responder keys
```

Field notes:
- `socket`: UDS path used by daemon control plane.
- `listen`: enables TCP listener. Requires `noise_key_file`.
- `noise_key_file`: static Noise private key file path. If file does not exist, daemon generates one with `0600` mode.
- `trusted_keys`: trusted peer public keys (hex). Used for Noise trust decisions.

### Service Fields

Common fields:

```toml
[[service]]
name = "gateway"
type = "singleton"   # singleton | worker
runtime = "binary"   # binary | docker
network = "uds"      # uds | tcp | dual
args = ["--config", "/etc/nexus/gateway.toml"]
depends_on = ["auth"]
health_check = "http://127.0.0.1:8080/healthz"
```

Binary runtime fields:

```toml
binary = "/opt/nexus/bin/gateway"
work_dir = "/opt/nexus"
env = { LOG_LEVEL = "info", MODE = "prod" }
```

Docker runtime fields:

```toml
image = "registry.example.com/postproc:1.2.3"
env = { APP_MODE = "prod" }
ports = ["8080:80", "8443:443/tcp"]
cap_add = ["NET_ADMIN"]
cap_drop = ["ALL"]
docker_network = "bridge"
extra_args = ["--restart", "always"]
volumes = ["/opt/data:/data"]
```

### Service Type Enforcement

Validation rules:
- `type = "singleton"`: `instances` must be empty.
- `type = "worker"`: `instances` must contain at least one instance.
- Unknown `type` is rejected.
- `runtime = "binary"`: `binary` is required and must be absolute path.
- `runtime = "docker"`: `image` is required.
- `work_dir` (if set) must be absolute path.

## Control Plane

Daemon control plane is exposed through `daemon.socket` (Unix domain socket).

Protocol:
- Frame: 4-byte big-endian length prefix + msgpack payload.
- Max message size: 64 MiB.

Commands:

| Command | Request | Response |
|---|---|---|
| `status` | `{cmd:"status"}` | `{services:[{name,id,pid,running}]}` |
| `health` | `{cmd:"health"}` | `{ok:true, uptime_seconds:n}` |
| `register` | `{cmd:"register", name, id, endpoints, capabilities, ttl_ms}` | `{ok:true}` |
| `unregister` | `{cmd:"unregister", id}` | `{ok:true}` |
| `heartbeat` | `{cmd:"heartbeat", id}` | `{ok:true}` or `{ok:false,error:"instance not found"}` |
| `lookup` | `{cmd:"lookup", name}` | `{instances:[...]}` |
| `watch` | `{cmd:"watch", name}` | First `{ok:true}`, then stream events |

Watch streaming behavior:
- After `watch`, connection stays open.
- Daemon pushes event frames: `{event:"up|down|...", instance:{...}}`.
- Multiple `watch` commands can be sent on the same connection.
- Closing connection unsubscribes all watchers for that session.

## Dependency Management (`depends_on`)

Startup order is resolved before process launch.

Rules:
- Uses Kahn's algorithm for topological sorting.
- Missing dependency causes startup error.
- Dependency cycle causes startup error (cycle path included in error text).
- For nodes with equal indegree, order remains deterministic using config order.

Example:

```toml
[[service]]
name = "gateway"

[[service]]
name = "auth"
depends_on = ["gateway"]

[[service]]
name = "worker"
depends_on = ["auth"]
```

Resulting startup order: `gateway -> auth -> worker`.

## Health Check (`health_check`)

When `health_check` is configured, daemon executes probe checks for each expanded instance.

Supported probe schemes:
- `exec://<command ...>`
- `http://...` (2xx = healthy)
- `tcp://host:port`

Behavior:
- If probe is configured and probe fails: instance is unhealthy and restart policy is applied.
- If probe is not configured: fallback to process liveness (PID-alive/running state) check.

Examples:

```toml
health_check = "exec:///usr/bin/test -f /tmp/ready"
health_check = "http://127.0.0.1:8080/healthz"
health_check = "tcp://127.0.0.1:6379"
```

## Transport Encryption (Noise)

Nexus secures TCP transport with Noise Protocol.

### Handshake Pattern

- Pattern: `NK`
- Cipher suite: `Curve25519 + ChaCha20-Poly1305 + SHA256`
- `NK` authenticates responder/server static key.

### Key Management

- Generate keypair with CLI: `nexusd keygen`.
- Daemon reads private key from `daemon.noise_key_file`.
- If file is missing, daemon can generate and persist a new private key (`0600` file mode).
- Public key is derived from private key and advertised/logged where needed.

### Trust Model

UDS trust model:
- Trust boundary is local filesystem permissions.
- Socket directory and socket mode protect local control-plane access.

TCP trust model:
- Trust is based on Noise responder static key verification.
- `trusted_keys` can restrict accepted remote responder keys.

### Wire Format

- Control plane over UDS: `4-byte big-endian length + msgpack`.
- Noise handshake messages: length-prefixed binary frames.
- Post-handshake data: msgpack envelope encrypted by Noise, sent as length-prefixed frames.

## SDK (Go)

v0.4 SDK updates:
- `Client` renamed to `Node`.
- Added `HandleFunc` convenience method.
- Added `RegistryAddr` for cross-process registry access via daemon control socket.

Example:

```go
node, err := sdk.New(sdk.Config{
    Name:         "image-processor",
    ID:           "image-processor-1",
    UDSAddr:      "/run/nexus/image-processor.sock",
    RegistryAddr: "/run/nexus/registry.sock",
})
if err != nil {
    panic(err)
}
defer node.Close()

node.HandleFunc("process", func(req *sdk.Request) (*sdk.Response, error) {
    return &sdk.Response{Payload: req.Payload}, nil
})

if err := node.Serve(); err != nil {
    panic(err)
}
```

## CLI

`nexusd` supports daemon mode and utility subcommands.

Subcommands:
- `nexusd validate -config ./nexus.toml`
  - Parse config and validate dependency graph.
- `nexusd status -socket /run/nexus/registry.sock`
  - Query daemon control socket and print service table.
- `nexusd keygen -out ./nexus.key`
  - Generate Noise keypair, write private key file, print public key.

## Removed in v0.4

- `daemon.peers` has been removed from active configuration.
- Peer-sync based registry topology is not part of v0.4 runtime/config schema.

## Build

```bash
CGO_ENABLED=0 go build ./...
go test ./...
```
