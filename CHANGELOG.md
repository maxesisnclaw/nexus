# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## v0.4 — 2026-03-07

### Added
- **Configuration Enhancement**: `work_dir`, `env` for binary services; `ports`, `cap_add`, `cap_drop`, `docker_network`, `extra_args` for Docker services; service type enforcement (singleton/worker validation)
- **Control Plane**: Daemon UDS control socket (`daemon.socket`) with msgpack protocol supporting `status`, `health`, `register`, `unregister`, `heartbeat`, `lookup`, `watch` commands
- **Dependency Ordering**: `depends_on` with Kahn's algorithm topological sort, cycle detection, missing dependency validation
- **Health Probes**: `health_check` supporting `exec://`, `http://`, `tcp://` probe types with fallback to PID-alive check
- **Noise Protocol Encryption**: TCP transport encryption using Noise NK pattern (ChaCha20-Poly1305, Curve25519); key generation and management; UDS trust model via filesystem permissions
- **CLI Subcommands**: `nexusd validate` (config validation), `nexusd status` (daemon status query), `nexusd keygen` (Noise keypair generation)
- **Python SDK**: Full-parity Python SDK with Node, registry client, FD passing, connection pool

### Changed
- **SDK API**: Renamed `Client` -> `Node` to align with mesh semantics; added `HandleFunc` convenience method; improved error context in all RPC paths
- **Remote Registry**: SDK supports `RegistryAddr` for cross-process service discovery via daemon control socket

### Removed
- **Peer Sync**: Removed `daemon.peers` configuration (planned for future version)

## [0.2.0] - 2026-03-06

### Added
- Docker-based Linux test workflow (`Dockerfile.test`) for race and integration test execution.
- Integration tests for SDK memfd path and daemon lifecycle (`-tags integration`).
- Extensive new unit tests across config, daemon, registry, transport, and SDK.
- Benchmark coverage for msgpack transport and memfd payload paths.
- Project infrastructure files: `Dockerfile`, `docker-compose.yml`, `Makefile`, CI workflow, release config.
- Runnable `examples/` programs: ping-pong, multi-stage pipeline, and docker-service example.

### Changed
- Process manager rollback behavior on partial startup failures.
- Daemon startup state reset on failed service startup.
- Listener `Accept` behavior now honors context cancellation for UDS/TCP.
- SDK request path now supports configurable retries and backoff.
- Registry and transport API docs and tests polished for production usage.

### Fixed
- Stale process map entries when child processes exit naturally.
- Error aggregation behavior when stopping multiple service instances.
- Several error-path edge cases in SDK/transport handling.

## [0.1.0] - 2026-03-06

### Added
- Initial Nexus daemon, transport, registry, and SDK implementation.
