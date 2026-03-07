# Nexus v0.4 Spec — Usability, Security Model & CLI

**Status**: Draft for review  
**Branch**: `feat/v0.4` (create from main before starting)  
**Constraint**: All changes must pass `go build ./...`, `go vet ./...`, `go test ./...`, `go test -race ./...` after each phase.

---

## Phase 1: Configuration Enhancement

### 1.1 Binary service: add `WorkDir` and `Env`

**File**: `pkg/config/types.go` — add to `ServiceSpec`:
```go
WorkDir string            `toml:"work_dir"`
Env     map[string]string `toml:"env"`
```

**File**: `pkg/daemon/process.go` — in `startBinaryProcess`:
- If `proc.Spec.WorkDir != ""`, set `cmd.Dir = proc.Spec.WorkDir`
- If `proc.Spec.WorkDir == ""`, default to `filepath.Dir(proc.Spec.Binary)`
- Merge `proc.Spec.Env` into `cmd.Env` (inherit `os.Environ()`, then overlay spec env)

**File**: `pkg/config/config.go` — in `validate`:
- If `work_dir` is set, it must be an absolute path

**Tests**:
- `config_test.go`: parse TOML with `work_dir` and `env`, validate defaults
- `process.go` test: verify `cmd.Dir` and `cmd.Env` are set correctly

### 1.2 Docker service: add `Env`, `Ports`, `CapAdd`, `CapDrop`, `DockerNetwork`, `ExtraArgs`

**File**: `pkg/config/types.go` — add to `ServiceSpec`:
```go
Env           map[string]string `toml:"env"`            // shared with binary
Ports         []string          `toml:"ports"`           // e.g. ["8080:80", "443:443"]
CapAdd        []string          `toml:"cap_add"`         // e.g. ["NET_ADMIN"]
CapDrop       []string          `toml:"cap_drop"`        // e.g. ["ALL"]
DockerNetwork string            `toml:"docker_network"`  // e.g. "host", "bridge", "none"
ExtraArgs     []string          `toml:"extra_args"`      // escape hatch: raw docker run flags
```

**File**: `pkg/daemon/docker.go` — in `Start`:
- Build args from structured fields in this order:
  1. `--name`
  2. `--network` (if DockerNetwork set)
  3. `-e KEY=VALUE` for each Env entry (sorted by key for determinism)
  4. `-v` for each Volume
  5. `-p` for each Port
  6. `--cap-add` / `--cap-drop`
  7. UDS volume mount (existing logic)
  8. `ExtraArgs...` (appended verbatim)
  9. Image + Args

**File**: `pkg/config/config.go` — in `validate`:
- `ports` format validation: must match `hostPort:containerPort` or `hostPort:containerPort/proto`
- `cap_add`/`cap_drop` values should be uppercase (warn or normalize)

**Tests**:
- `docker_test.go`: verify generated `docker run` command args for various config combos
- `config_test.go`: parse TOML with all new docker fields

### 1.3 Service type enforcement

**File**: `pkg/config/config.go` — in `validate`:
- If `type = ""`, default to `"singleton"`
- If `type = "worker"` and `len(instances) == 0`, return error: "worker service %s requires at least one instance"
- If `type = "singleton"` and `len(instances) > 0`, return error: "singleton service %s must not define instances"

**Tests**: config validation test cases for type/instances combinations

### 1.4 Config example file

**File**: `nexus.example.toml` — create a fully commented example showing ALL fields with their defaults and explanations. This is the "documentation by example" that ops personnel will actually read.

---

## Phase 2: Reserved Fields — Implement or Remove

### 2.1 Implement `daemon.socket` (control plane)

**New file**: `pkg/daemon/control.go`

**Protocol**: Each message is a length-prefixed (4-byte big-endian) msgpack object. This matches the service transport format and allows any SDK (Go, Python, future) to reuse the same codec.

**Commands — Daemon Management**:
| Command | Request | Response |
|---------|---------|----------|
| `status` | `{"cmd":"status"}` | `{"services":[{"name":"...","id":"...","pid":123,"running":true},...]}` |
| `health` | `{"cmd":"health"}` | `{"ok":true,"uptime_seconds":3600}` |

**Commands — Registry Operations** (critical for multi-language SDK support):
| Command | Request | Response |
|---------|---------|----------|
| `register` | `{"cmd":"register","name":"svc","id":"svc-1","endpoints":[{"type":"uds","addr":"/tmp/x.sock"}],"capabilities":["cap"],"ttl_ms":15000}` | `{"ok":true}` |
| `unregister` | `{"cmd":"unregister","id":"svc-1"}` | `{"ok":true}` |
| `heartbeat` | `{"cmd":"heartbeat","id":"svc-1"}` | `{"ok":true}` |
| `lookup` | `{"cmd":"lookup","name":"svc"}` | `{"instances":[...]}` |
| `watch` | `{"cmd":"watch","name":"svc"}` | Stream: `{"event":"up","instance":{...}}` pushed as they occur |

**Watch streaming**: After sending a `watch` command, the connection stays open and the daemon pushes `ChangeEvent` objects as they happen. The client can watch multiple services by sending multiple `watch` commands on the same connection.

**Why this matters**: The Go SDK currently uses an in-process `registry.New()`. Cross-process and cross-language SDKs (Python, future Rust/C++) cannot share Go memory — they MUST talk to the daemon's registry over this socket. This is the multi-language enabler.

**New file**: `pkg/daemon/control.go`

```go
type ControlServer struct {
    listener net.Listener
    daemon   *Daemon
    registry *registry.Registry
    logger   *slog.Logger
}
```

**Integration**:
- `daemon.go` `Start()`: launch control server on `cfg.Daemon.Socket`
- `daemon.go` `Stop()`: close control server
- Socket permissions: `0600` (same as service UDS)
- The daemon's own `Registry` instance is shared with the control server
- Go SDK must also support connecting to daemon registry via socket: add `RegistryAddr string` to `sdk.Config` as an alternative to passing a `Registry` instance. When `RegistryAddr` is set and `Registry` is nil, the SDK connects to the daemon's control socket for register/heartbeat/lookup/watch instead of using an in-process registry. This is how cross-process Go services discover each other without sharing memory.

**Tests**:
- Start control server, connect, send status command, verify response
- Register via control socket, lookup via control socket, verify instance returned
- Watch: register → verify watch client receives event
- Heartbeat: register with short TTL, heartbeat, verify not expired

### 2.2 Implement `service.depends_on` (topological startup)

**File**: `pkg/daemon/daemon.go`

Replace sequential startup with dependency-aware ordering:

```go
func resolveStartOrder(services []config.ServiceSpec) ([]config.ServiceSpec, error) {
    // Build DAG from depends_on references
    // Topological sort (Kahn's algorithm)
    // Return error on cycle: "circular dependency: A -> B -> C -> A"
    // Return error on missing dep: "service %s depends on unknown service %s"
}
```

**Integration**: call `resolveStartOrder` in `Start()` before the service launch loop.

**Tests**:
- Linear chain: A → B → C starts in order C, B, A
- Diamond: A → {B, C}, B → D, C → D starts D first
- Cycle detection: A → B → A returns error
- Missing dependency: A → nonexistent returns error
- No dependencies: preserves config file order

### 2.3 Implement `service.health_check` (probe-based health)

**File**: `pkg/daemon/health.go`

Add probe types:

```go
type HealthProbe interface {
    Check(ctx context.Context) error
}

type execProbe struct {
    command string
    args    []string
}

type httpProbe struct {
    url string
}

type tcpProbe struct {
    addr string
}
```

**Parsing** (in config or health.go):
- `"exec:///path/to/script arg1 arg2"` → execProbe
- `"http://host:port/path"` → httpProbe (GET, expect 2xx)
- `"tcp://host:port"` → tcpProbe (dial, expect connect)

**Integration**: `HealthMonitor.checkOnce` should:
1. If `health_check` is configured → use the probe
2. If not configured → fall back to existing PID-alive check

**Tests**: unit test each probe type with mock/stub

### 2.4 Remove `daemon.peers`

**Files**: `pkg/config/types.go`, `pkg/config/config.go`, `DESIGN.md`, `README.md`

- Remove `Peers []PeerConfig` from `DaemonConfig`
- Remove `PeerConfig` struct
- Remove any references in docs
- Add a comment in `DaemonConfig`: `// Peer-based registry sync is planned for a future version.`

### 2.5 Implement `daemon.listen` skeleton

**File**: `pkg/config/types.go` — `Listen` field already exists, keep it.

**File**: `pkg/daemon/daemon.go` — in `Start()`:
- If `cfg.Daemon.Listen != ""`, start a TCP listener (Noise-encrypted, see Phase 4)
- For now (Phase 2), just validate the address format and log a message: "TCP control plane listener configured but Noise transport not yet initialized"
- The actual Noise listener wiring happens in Phase 4

---

## Phase 3: API Redesign

### 3.1 Rename `Client` → `Node`

**Rationale**: `Client` is misleading — it's both server and client. `Node` maps to the service mesh concept (a participant in the network) and aligns with `registry.NodeID()`.

**File**: `pkg/sdk/client.go`
- Rename struct `Client` → `Node`
- Rename `clientState` → `nodeState`, `clientNew/clientServing/clientClosed` → `nodeNew/nodeServing/nodeClosed`
- Keep `sdk.New()` as the constructor (returns `*Node`)
- Update all internal method receivers from `c *Client` to `n *Node`

**File**: `pkg/sdk/client_test.go`, `pkg/sdk/client_integration_linux_test.go`
- Update all references

**File**: `examples/ping-pong/main.go`, `examples/pipeline/main.go`, `examples/docker-service/main.go`
- Update variable names (cosmetic — the type name change propagates through `sdk.New`)

**Important**: This is a breaking API change. All references in README.md, DESIGN.md must be updated.

**Clarification**: `sdk.Config.Network` (transport mode for this node's listeners: uds/tcp/dual) vs `config.ServiceSpec.Network` (same concept but in daemon config TOML). They serve the same purpose at different layers. When the daemon starts a service, it passes the configured `Network` through. Add a doc comment to both fields making this relationship explicit.

### 3.2 Add `HandleFunc` convenience method

**File**: `pkg/sdk/client.go` (now node.go after rename)

Add alongside existing `Handle`:

```go
// HandleFunc registers a simple bytes-in/bytes-out handler.
func (n *Node) HandleFunc(method string, fn func(payload []byte) ([]byte, error)) {
    n.Handle(method, func(req *Request) (*Response, error) {
        result, err := fn(req.Payload)
        if err != nil {
            return nil, err
        }
        return &Response{Payload: result}, nil
    })
}
```

This does NOT replace `Handle` — power users still use `Handle` for headers/metadata access.

**Update examples**: Convert the simple examples to use `HandleFunc` to demonstrate the simpler API.

### 3.3 Rename source file

- Rename `pkg/sdk/client.go` → `pkg/sdk/node.go`
- Rename `pkg/sdk/client_test.go` → `pkg/sdk/node_test.go`
- Rename `pkg/sdk/client_integration_linux_test.go` → `pkg/sdk/node_integration_linux_test.go`

### 3.4 Improve error context in Call failures

**File**: `pkg/sdk/node.go` — in `callOnce` / `callOnceCtx`:

When returning errors, wrap with context:
```go
return nil, fmt.Errorf("call %s.%s (instance=%s, transport=%s): %w",
    serviceName, method, inst.ID, transportType, err)
```

Where `transportType` is "uds" or "tcp" based on the resolved endpoint.

---

## Phase 4: Security Model

### 4.1 UDS trust model — skip AuthFunc

**File**: `pkg/sdk/node.go` — in `serveConn` (the per-connection handler):

Currently `AuthFunc` is called for every request. Change to:
- Add a `connIsLocal bool` field or parameter to `serveConn`
- When the connection comes from a UDS listener, set `connIsLocal = true`
- Skip `AuthFunc` when `connIsLocal == true`

**Implementation approach**: In the accept loop, track which listener accepted the connection. UDS listeners → local. TCP listeners → remote.

Modify the `acceptResult` struct:
```go
type acceptResult struct {
    conn  transport.Conn
    err   error
    local bool  // true if from UDS listener
}
```

In `serveConn`, pass `local` through and skip auth for local connections.

### 4.2 Noise Protocol transport

**New dependency**: `github.com/flynn/noise` (mature, used by Flynn project, Curve25519+ChaChaPoly+BLAKE2)

**New file**: `pkg/transport/noise.go`

```go
// NoiseTransport wraps a TCP transport with Noise Protocol encryption.
type NoiseTransport struct {
    inner      *TCPTransport
    staticKey  noise.DHKey
    peerKeys   map[string][]byte  // addr → peer public key
    psk        []byte             // optional pre-shared key
}

func NewNoiseTransport(staticKey noise.DHKey, opts ...NoiseOption) *NoiseTransport
```

**Noise pattern**: `IK` (initiator knows responder's static key)
- Dialer sends its static key, already knows the peer's public key from registry/config
- 1-RTT handshake
- All subsequent data is encrypted
- Traffic is indistinguishable from random bytes (no TLS headers, no protocol markers)

**Connection wrapping**:
```go
// noiseConn wraps a net.Conn with Noise encryption
type noiseConn struct {
    raw       net.Conn
    cipher    *noise.CipherState  // for sending
    decipher  *noise.CipherState  // for receiving
    sendMu    sync.Mutex
    recvMu    sync.Mutex
}

func (c *noiseConn) Read(p []byte) (int, error)   // decrypt
func (c *noiseConn) Write(p []byte) (int, error)  // encrypt
```

Since `noiseConn` implements `net.Conn`, it can be passed directly to `newMsgpackConn` — the msgpack layer doesn't need to know about encryption.

**Message framing**: Use 2-byte big-endian length prefix for each encrypted frame. Max frame size = 65535 bytes (Noise protocol max message size). Larger messages are split into multiple frames.

**Key format in config**:
```toml
[daemon]
listen = "0.0.0.0:7700"

[daemon.noise]
private_key = "base64-encoded-32-byte-key"
psk = "base64-encoded-32-byte-psk"  # optional, extra layer
```

**Key generation helper**:
```bash
nexusd keygen  # prints a new keypair to stdout
```

**File**: `pkg/config/types.go` — add:
```go
type NoiseConfig struct {
    PrivateKey string `toml:"private_key"`
    PSK        string `toml:"psk"`
}
```

Add `Noise *NoiseConfig` to `DaemonConfig`.

### 4.3 Router integration

**File**: `pkg/transport/transport.go` — `Router.Dial`:

Current logic: if local+UDS → use UDS transport, else if TCPAddr → use TCP transport.

New logic: if local+UDS → use UDS (plain), else if TCPAddr → use Noise TCP transport. The router should hold both `uds Transport` and `tcp Transport` where `tcp` is the Noise-wrapped transport when configured.

**File**: `pkg/daemon/daemon.go` — when `daemon.listen` is set and Noise config is present:
- Start Noise TCP listener on `daemon.listen`
- Accept connections, perform Noise handshake, then hand off to control server or service routing

### 4.4 Remove insecure TCP fallback

**File**: `pkg/transport/tcp.go`:
- Remove `NEXUS_ALLOW_INSECURE_TCP_LISTEN` env var and `allowsInsecureTCPListen()` check
- Plain TCP transport should ONLY be usable for loopback addresses (hard-coded, no env override)
- For non-loopback, Noise transport is required

**Tests**:
- Noise handshake unit test: two in-memory pipes, verify encrypted data round-trip
- Verify plain TCP rejects non-loopback without Noise
- Verify DPI resistance: captured bytes should have no recognizable protocol headers (statistical randomness test — entropy > 7.9 bits/byte on handshake+first message)

---

## Phase 5: CLI Subcommands

### 5.1 Refactor `cmd/nexusd/main.go` to support subcommands

Current: `nexusd -config ./nexus.toml` (only one mode — run daemon)

New structure:
```
nexusd                          # default: run daemon (backward compat)
nexusd run -config ./nexus.toml # explicit run
nexusd validate -config ./nexus.toml  # validate config and exit
nexusd status -socket /run/nexus/registry.sock  # query running daemon
nexusd keygen                   # generate Noise keypair
```

Use `flag` subcommand pattern (no external deps):

```go
func main() {
    if len(os.Args) > 1 {
        switch os.Args[1] {
        case "run":
            os.Exit(runDaemon(os.Args[2:]))
        case "validate":
            os.Exit(runValidate(os.Args[2:]))
        case "status":
            os.Exit(runStatus(os.Args[2:]))
        case "keygen":
            os.Exit(runKeygen(os.Args[2:]))
        default:
            // If first arg starts with "-", treat as legacy mode
            if strings.HasPrefix(os.Args[1], "-") {
                os.Exit(runDaemon(os.Args[1:]))
            }
            fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
            os.Exit(2)
        }
    }
    os.Exit(runDaemon(os.Args[1:]))
}
```

### 5.2 `nexusd validate`

```go
func runValidate(args []string) int {
    flags := flag.NewFlagSet("validate", flag.ContinueOnError)
    var configPath string
    flags.StringVar(&configPath, "config", "./nexus.toml", "config path")
    if err := flags.Parse(args); err != nil {
        return 2
    }
    cfg, err := config.Load(configPath)
    if err != nil {
        fmt.Fprintf(os.Stderr, "INVALID: %v\n", err)
        return 1
    }
    // Additional semantic validation
    if _, err := daemon.ResolveStartOrder(cfg.Services); err != nil {
        fmt.Fprintf(os.Stderr, "INVALID: %v\n", err)
        return 1
    }
    fmt.Printf("OK: %d services configured\n", len(cfg.Services))
    return 0
}
```

### 5.3 `nexusd status`

```go
func runStatus(args []string) int {
    flags := flag.NewFlagSet("status", flag.ContinueOnError)
    var socketPath string
    flags.StringVar(&socketPath, "socket", "/run/nexus/registry.sock", "daemon control socket")
    if err := flags.Parse(args); err != nil {
        return 2
    }
    // Connect to control socket, send {"cmd":"status"}, print table
    conn, err := net.Dial("unix", socketPath)
    // ... send command, read response, format as table
    // Print: SERVICE  ID  PID  STATUS
}
```

### 5.4 `nexusd keygen`

```go
func runKeygen(args []string) int {
    key, _ := noise.DH25519.GenerateKeypair(rand.Reader)
    fmt.Printf("private_key = %q\n", base64.StdEncoding.EncodeToString(key.Private))
    fmt.Printf("public_key  = %q\n", base64.StdEncoding.EncodeToString(key.Public))
    return 0
}
```

---

## Phase 6: UX Polish

### 6.1 Docker failure diagnostics

**File**: `pkg/daemon/docker.go` — in `Start`:

When `docker run` fails, automatically capture the last 50 lines of container logs:
```go
if err != nil {
    // Try to get logs for diagnostics
    logCmd := execCommandContext(ctx, "docker", "logs", "--tail", "50", name)
    logOut, _ := logCmd.CombinedOutput()
    if len(logOut) > 0 {
        return "", fmt.Errorf("docker run failed: %w\ncontainer logs:\n%s", err, bytes.TrimSpace(logOut))
    }
    return "", fmt.Errorf("docker run failed: %w (%s)", err, bytes.TrimSpace(out))
}
```

### 6.2 Startup summary log

**File**: `pkg/daemon/daemon.go` — after all services started:

```go
logger.Info("all services started",
    "count", len(cfg.Services),
    "services", serviceNames,
    "startup_duration", time.Since(startTime))
```

### 6.3 Update DESIGN.md

- Remove all "reserved/not implemented" caveats for fields that are now implemented
- Update Phase checklist to reflect v0.4 completions
- Add Phase 4 Docker as completed
- Add new Noise encryption section
- Update architecture diagram to show Noise layer

### 6.4 Update README.md

- Update SDK examples to use `Node` instead of `Client`
- Add `HandleFunc` example
- Add `nexusd validate` and `nexusd status` documentation
- Update configuration reference with all new fields
- Add security model section (UDS trust / Noise TCP)
- Remove `NEXUS_ALLOW_INSECURE_TCP_LISTEN` references

### 6.5 Update CHANGELOG.md

Add `[0.4.0]` section covering all changes.

### 6.6 Create `nexus.example.toml`

Fully commented example config showing every field:

```toml
# Nexus Daemon Configuration
# Copy this file to nexus.toml and adjust for your environment.

[daemon]
# Control plane socket for nexusd status/management commands
socket = "/run/nexus/registry.sock"

# Log level: debug, info, warn, error
log_level = "info"

# Health check interval
health_interval = "5s"

# Graceful shutdown timeout before SIGKILL
shutdown_grace = "10s"

# TCP listener for cross-node communication (requires [daemon.noise])
# listen = "0.0.0.0:7700"

# Noise Protocol encryption for TCP transport
# Generate keys with: nexusd keygen
# [daemon.noise]
# private_key = "base64-encoded-private-key"
# psk = "base64-encoded-preshared-key"  # optional extra authentication

# --- Services ---

[[service]]
name = "my-api"
type = "singleton"           # "singleton" (default) or "worker"
runtime = "binary"           # "binary" (default) or "docker"
binary = "/usr/local/bin/my-api"
args = ["--port", "8080"]
work_dir = "/opt/my-api"     # default: directory of binary
network = "uds"              # "uds" (default), "tcp", or "dual"
depends_on = []              # service names that must start first
health_check = ""            # "exec://...", "http://...", or "tcp://..."

# Environment variables (works for both binary and docker)
# Use inline table for env within [[service]] array
env = { DATABASE_URL = "postgres://localhost/mydb", LOG_LEVEL = "info" }

# [[service]]
# name = "my-worker"
# type = "worker"
# runtime = "docker"
# image = "my-worker:latest"
# network = "uds"
# ports = ["8080:80"]
# cap_add = ["SYS_PTRACE"]
# cap_drop = []
# docker_network = "bridge"  # Docker network mode
# extra_args = ["--gpus", "all"]  # Raw docker run flags
# volumes = ["/data:/data"]
# depends_on = ["my-api"]
# health_check = "http://localhost:8080/healthz"
#
# [[service.instances]]
# id = "worker-1"
# args = ["--shard", "a"]
#
# [[service.instances]]
# id = "worker-2"
# args = ["--shard", "b"]
```

---

## Execution Order for Supervisor

The supervisor should execute phases sequentially. Within each phase, all changes should be committed as one atomic commit.

| Phase | Commit message | Key risk |
|-------|---------------|----------|
| 1 | `feat(config): add WorkDir, Env, Ports, CapAdd, ExtraArgs, type enforcement` | Config parsing compat |
| 2 | `feat(daemon): control socket, depends_on ordering, health probes; remove peers` | DAG sort correctness |
| 3 | `refactor(sdk): rename Client→Node, add HandleFunc, improve error context` | Breaking API, must update all refs |
| 4 | `feat(transport): Noise Protocol encryption for TCP, UDS trust model` | Crypto correctness, DPI resistance |
| 5 | `feat(cli): validate, status, keygen subcommands` | Backward compat with bare `nexusd` |
| 6 | `docs: update DESIGN.md, README.md, CHANGELOG.md, add example config` | None (docs only) |

After each phase: `go build ./...` + `go vet ./...` + `go test ./...` + `go test -race ./...`  
After all phases: push branch, open for review.

---

## Phase 7: Python SDK — Full Parity

**Goal**: Python SDK must be feature-equivalent to the Go SDK. Key use case: data analysis and image processing teams use Python, need zero-copy FD passing for large image payloads.

### 7.1 Package structure

Restructure from single file to proper Python package:

```
pkg/sdk/python/
  pyproject.toml
  src/
    nexus_sdk/
      __init__.py        # re-export Node, Request, Response, BusinessError
      node.py            # Main Node class
      registry_client.py # Registry client (connects to daemon control socket)
      transport.py       # UDS/TCP msgpack transport
      fd.py              # memfd + FD passing utilities
      types.py           # Request, Response, BusinessError
      noise.py           # Noise encryption (wraps noise-c or dissononce)
  tests/
    conftest.py
    test_node.py
    test_registry_client.py
    test_transport.py
    test_fd.py
```

**pyproject.toml**:
```toml
[project]
name = "nexus-sdk"
version = "0.4.0"
requires-python = ">=3.10"
dependencies = [
    "msgpack>=1.0",
]

[project.optional-dependencies]
noise = ["dissononce>=0.34"]

[build-system]
requires = ["hatchling"]
build-backend = "hatchling.build"
```

### 7.2 Core types (`types.py`)

```python
from dataclasses import dataclass, field

@dataclass
class Request:
    method: str
    payload: bytes
    headers: dict[str, str] = field(default_factory=dict)

@dataclass
class Response:
    payload: bytes
    headers: dict[str, str] = field(default_factory=dict)

class BusinessError(Exception):
    """Remote handler returned an error (not a transport failure). Will not be retried."""
    def __init__(self, message: str):
        self.message = message
        super().__init__(message)
```

### 7.3 Transport layer (`transport.py`)

```python
class MsgpackConn:
    """Send/receive msgpack messages over a socket with size limiting."""
    def __init__(self, sock: socket.socket, max_msg_size: int = 64 * 1024 * 1024):
        ...
    def send(self, msg: dict) -> None: ...
    def recv(self) -> dict: ...
    def set_read_deadline(self, timeout: float | None) -> None: ...
    def set_write_deadline(self, timeout: float | None) -> None: ...
    def close(self) -> None: ...

class UDSTransport:
    def dial(self, addr: str) -> MsgpackConn: ...
    def listen(self, addr: str) -> UDSListener: ...

class TCPTransport:
    def dial(self, addr: str) -> MsgpackConn: ...
    def listen(self, addr: str) -> TCPListener: ...
```

### 7.4 FD passing (`fd.py`) — CRITICAL for image processing

```python
import os
import socket
import array

def create_memfd(name: str, payload: bytes) -> int:
    """Create anonymous in-memory file with payload using os.memfd_create (Linux 3.17+)."""
    fd = os.memfd_create(name, os.MFD_CLOEXEC)
    os.write(fd, payload)
    os.lseek(fd, 0, os.SEEK_SET)
    # dup with CLOEXEC
    new_fd = os.dup(fd)
    os.set_inheritable(new_fd, False)
    os.close(fd)
    return new_fd

def send_fd(sock: socket.socket, fd: int, metadata: bytes = b'\x00') -> None:
    """Send file descriptor over Unix socket using SCM_RIGHTS."""
    fds = array.array('i', [fd])
    sock.sendmsg([metadata], [(socket.SOL_SOCKET, socket.SCM_RIGHTS, fds)])

def recv_fd(sock: socket.socket, max_metadata: int = 65536) -> tuple[int, bytes]:
    """Receive file descriptor from Unix socket."""
    msg, ancdata, flags, addr = sock.recvmsg(max_metadata, socket.CMSG_SPACE(4 * 16))
    for cmsg_level, cmsg_type, cmsg_data in ancdata:
        if cmsg_level == socket.SOL_SOCKET and cmsg_type == socket.SCM_RIGHTS:
            fds = array.array('i')
            fds.frombytes(cmsg_data[:len(cmsg_data) - (len(cmsg_data) % fds.itemsize)])
            if fds:
                # Close extra FDs, keep first
                for extra_fd in fds[1:]:
                    os.close(extra_fd)
                return fds[0], msg
    raise RuntimeError("no fd received")

def read_fd_all(fd: int, max_bytes: int) -> bytes:
    """Read all bytes from fd with size limit."""
    dup_fd = os.dup(fd)
    os.lseek(dup_fd, 0, os.SEEK_SET)
    try:
        with os.fdopen(dup_fd, 'rb') as f:
            data = f.read(max_bytes + 1)
            if len(data) > max_bytes:
                raise ValueError(f"fd payload exceeds limit of {max_bytes} bytes")
            return data
    except Exception:
        # dup_fd is closed by fdopen context manager
        raise
```

### 7.5 Registry client (`registry_client.py`)

Connects to daemon control socket for service discovery:

```python
class RegistryClient:
    """Client for nexusd registry protocol over control socket."""

    def __init__(self, socket_path: str = "/run/nexus/registry.sock"):
        self._sock_path = socket_path
        self._conn: MsgpackConn | None = None

    def connect(self) -> None: ...
    def close(self) -> None: ...
    def register(self, name: str, id: str, endpoints: list[dict],
                 capabilities: list[str] = None, ttl_ms: int = 15000) -> None: ...
    def unregister(self, id: str) -> None: ...
    def heartbeat(self, id: str) -> None: ...
    def lookup(self, name: str) -> list[dict]: ...
    def watch(self, name: str, callback: Callable) -> Callable:
        """Subscribe to changes. Returns unsubscribe function."""
        ...
```

### 7.6 Node class (`node.py`) — full parity with Go SDK

```python
class Node:
    def __init__(
        self,
        name: str,
        *,
        id: str | None = None,               # default: name
        daemon_socket: str = "/run/nexus/registry.sock",
        uds_addr: str | None = None,          # listen address
        tcp_addr: str | None = None,
        network: str = "uds",                 # uds / tcp / dual
        request_timeout: float = 5.0,         # seconds
        serve_timeout: float = 30.0,
        large_payload_threshold: int = 1 << 20,  # 1MB
        call_retries: int = 0,
        retry_backoff: float = 0.1,
        max_inbound_conns: int = 128,
        auth_func: Callable[[Request], None] | None = None,
        logger: logging.Logger | None = None,
    ): ...

    def handle(self, method: str) -> Callable:
        """Decorator: register full handler (Request → Response)."""
        ...

    def handle_func(self, method: str) -> Callable:
        """Decorator: register simple handler (bytes → bytes)."""
        ...

    def serve(self, *, block: bool = True) -> None:
        """Start listening. Registers with daemon, starts heartbeat thread.
        If block=True, blocks until close(). If False, runs in background thread."""
        ...

    def call(self, service_name: str, method: str, payload: bytes) -> Response:
        """Call a remote service method. Auto-discovers via daemon registry."""
        ...

    def call_with_data(self, service_name: str, method: str, payload: bytes) -> Response:
        """Like call(), but uses FD passing for large payloads on local UDS.
        Falls back to regular call for TCP or small payloads."""
        ...

    def close(self) -> None:
        """Unregister from daemon, close listeners, drain connections."""
        ...
```

**Usage example** (image processing):
```python
from nexus_sdk import Node, Request, Response

# Image processing service
node = Node("image-processor", uds_addr="/run/nexus/imgproc.sock")

@node.handle("resize")
def resize(req: Request) -> Response:
    # req.payload is the image bytes (received via FD for large images)
    img = Image.open(io.BytesIO(req.payload))
    img = img.resize((256, 256))
    buf = io.BytesIO()
    img.save(buf, format="PNG")
    return Response(payload=buf.getvalue())

node.serve()
```

```python
# Caller (data analysis script)
from nexus_sdk import Node

node = Node("analyzer")
with open("big_photo.jpg", "rb") as f:
    image_data = f.read()  # 10MB image

# Automatically uses memfd + FD passing (> 1MB threshold)
result = node.call_with_data("image-processor", "resize", image_data)
with open("resized.png", "wb") as f:
    f.write(result.payload)
node.close()
```

### 7.7 Heartbeat thread

When `serve()` is called:
1. Register with daemon via `RegistryClient`
2. Start background thread: send `heartbeat` every `ttl / 3` seconds
3. On `close()`: send `unregister`, stop heartbeat

### 7.8 Connection handling

- Accept loop in dedicated thread per listener
- Per-connection thread for serving (matches current design)
- Semaphore for `max_inbound_conns`
- `serve_conn` handles:
  1. Auth check (skip for UDS connections, call `auth_func` for TCP)
  2. Panic recovery (catch exceptions, send error response, don't crash server)
  3. FD-based call detection (method == `__nexus_fd_call__`)
  4. Read/write timeouts via `socket.settimeout()`

### 7.9 Connection pool for outbound calls

```python
class ConnectionPool:
    """Thread-safe connection pool with per-endpoint idle limit."""
    def __init__(self, max_idle_per_endpoint: int = 4): ...
    def acquire(self, endpoint: dict) -> MsgpackConn: ...
    def release(self, endpoint: dict, conn: MsgpackConn, reusable: bool) -> None: ...
    def close(self) -> None: ...
```

### 7.10 Retry logic

Same as Go SDK:
- On call failure, retry up to `call_retries` times
- `BusinessError` → do NOT retry (handler returned an error, not transport)
- Transport error → retry with `retry_backoff` delay

### 7.11 Tests

- `test_types.py`: Request/Response construction, BusinessError
- `test_transport.py`: MsgpackConn send/recv over socketpair
- `test_fd.py`: create_memfd, send_fd/recv_fd over UDS socketpair, read_fd_all with size limit
- `test_node.py`: full round-trip — register handler, serve in background, call, verify response
- `test_node_fd.py`: large payload round-trip — call_with_data with payload > threshold, verify FD path used
- `test_registry_client.py`: mock control socket, test register/lookup/heartbeat/watch

All tests runnable with `pytest` from `pkg/sdk/python/`.

**CI integration**: FD/memfd tests require Linux. Add a step to `.github/workflows/ci.yml`:
```yaml
- name: Python SDK tests
  run: |
    docker run --rm -v ${{ github.workspace }}:/workspace -w /workspace/pkg/sdk/python \
      nexus-test bash -c "pip install -e '.[noise]' && pip install pytest && pytest -v"
```
This reuses the existing `nexus-test` Docker image (just needs Python 3.10+ installed — add to `Dockerfile.test`).

### 7.12 Noise encryption (optional install)

When `dissononce` is installed (`pip install nexus-sdk[noise]`):
- `noise.py` wraps TCP connections with Noise `IK` pattern
- `Node(..., noise_private_key=..., noise_psk=...)` enables encrypted TCP
- Without `noise` extra, TCP is loopback-only (same restriction as Go)

---

## Execution Order Update

| Phase | Commit message | Key risk |
|-------|---------------|----------|
| 1 | `feat(config): WorkDir, Env, Ports, CapAdd, ExtraArgs, type enforcement` | Config parsing compat |
| 2 | `feat(daemon): control socket with registry protocol, depends_on, health probes; remove peers` | Protocol design, DAG sort |
| 3 | `refactor(sdk): rename Client→Node, add HandleFunc, improve error context` | Breaking API |
| 4 | `feat(transport): Noise Protocol encryption for TCP, UDS trust model` | Crypto correctness |
| 5 | `feat(cli): validate, status, keygen subcommands` | Backward compat |
| 6 | `docs: DESIGN.md, README.md, CHANGELOG.md, example config` | None |
| 7 | `feat(python-sdk): full parity — Node, registry client, FD passing, conn pool` | Cross-language protocol compat |

After Phase 7: run both Go tests AND Python tests.

---

## Out of Scope (Future)

- Hot config reload (`nexusd reload`)
- Metrics/Prometheus endpoint
- `nexusd --dry-run` diagnostic mode
- Peer-to-peer registry sync protocol
- Streaming RPC
