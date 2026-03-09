# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.0.1](https://github.com/maxesisnclaw/nexus/compare/v1.0.0...v1.0.1) (2026-03-09)


### Bug Fixes

* **ci:** fetch full git history for goreleaser to find tags ([2c0007f](https://github.com/maxesisnclaw/nexus/commit/2c0007f1dc96a90938a11d5f9057f621793779a0))

## 1.0.0 (2026-03-09)


### Features

* add release-please automation and remove python-sdk job from CI ([3320f48](https://github.com/maxesisnclaw/nexus/commit/3320f489be750a10d866c7f4a3aedc80921f8cd4))
* **cli:** validate, status, keygen subcommands ([f6ffa72](https://github.com/maxesisnclaw/nexus/commit/f6ffa725dd8081d8a8fbff90c0f23f38588a9e18))
* **config:** WorkDir, Env, Ports, CapAdd, ExtraArgs, type enforcement ([aa37521](https://github.com/maxesisnclaw/nexus/commit/aa375212827b7f6460ee80b5f95812003a39265e))
* **daemon:** control socket with registry protocol, depends_on, health probes; remove peers ([e98e0cc](https://github.com/maxesisnclaw/nexus/commit/e98e0ccc8c6e9d3589690396ea6f05d3112ed75e))
* **python-sdk:** full parity — Node, registry client, FD passing, conn pool ([0a0ead1](https://github.com/maxesisnclaw/nexus/commit/0a0ead1ea946689820254f5b63dba2649b4978cd))
* **transport:** Noise Protocol encryption for TCP, UDS trust model ([8151b37](https://github.com/maxesisnclaw/nexus/commit/8151b37005e0f121d59b1cf1645e96ecba54f274))


### Bug Fixes

* address all 10 code review issues ([6d3c95e](https://github.com/maxesisnclaw/nexus/commit/6d3c95e30309d0c79a884563b8495a9b6ddc1f4a))
* **audit-r2:** bound shutdown and reserve daemon tcp listen ([dc79b00](https://github.com/maxesisnclaw/nexus/commit/dc79b00ced310a36f5196425a4bccca2816f20d8))
* **audit-r2:** C-1 inbound conn limit, C-2 safe pgid fallback, M-1 ctx check before binary start ([3501d85](https://github.com/maxesisnclaw/nexus/commit/3501d85776c593fd288a553c2e21f152b1a41630))
* **audit-r2:** harden cleanup and startup edge cases ([f040a59](https://github.com/maxesisnclaw/nexus/commit/f040a59dd409d7c5b6506e5594527d3a3b3887d2))
* **audit-r2:** harden noise key and python ipv6 dial ([6432be8](https://github.com/maxesisnclaw/nexus/commit/6432be8e7f4fb88ad7dffe178c1bdd102a766e0c))
* **audit-r2:** isolate registry panic reporter and enable go in python ci ([2c3e936](https://github.com/maxesisnclaw/nexus/commit/2c3e936d7e24e0f13c979b5d31325a5f07e26fa3))
* **audit-r2:** M-2 active conn tracking, M-3 send error handling, M-4 CI caps, m-1/m-2/m-3 ([18ebde7](https://github.com/maxesisnclaw/nexus/commit/18ebde707537349d1a7095858de867d69656b7f3))
* **audit-r2:** recover python registrations and unify control limits ([6729479](https://github.com/maxesisnclaw/nexus/commit/672947909b17adb6ee3d953659312deecb255f2d))
* **audit-r2:** secure go tcp dial and preserve fd fallback timeout ([cd59c02](https://github.com/maxesisnclaw/nexus/commit/cd59c02604a526784c0386b50f7058b2f3119b75))
* **audit-r3:** C-1 auth hook, M-2 business error no-retry, M-4 client state machine ([81a48a0](https://github.com/maxesisnclaw/nexus/commit/81a48a0b42d6b025f775da9bc347d14c75f483e6))
* **audit-r3:** C-2 FD size limit, M-3 CLOEXEC dup, M-1 request deadline, m-2 README caps, m-3 network validation ([be8d56d](https://github.com/maxesisnclaw/nexus/commit/be8d56d28ee9730c72eb02a97d3f1ca6ef2763ed))
* **audit-r4:** C-1 abs binary path, M-1 strict UDS mode, M-2 wg race, M-3 docker grace ceil, m-2 goreleaser hooks ([1ec2600](https://github.com/maxesisnclaw/nexus/commit/1ec2600433e057d5165a0e8af1d6c0ae21b2c758))
* **audit-r5:** C-1 panic recovery, C-2 lower conn limit, M-1 docker stop idempotent, M-2 registry ID validation, M-3 timestamp clamp, m-2 CI cap comments ([062e47e](https://github.com/maxesisnclaw/nexus/commit/062e47ef01cc995a2d792a3b68f63565fa4217c1))
* **audit-r6:** M-1 write deadline on Conn, M-2 docker stop verification, m-1 nil logger defense ([e2db3a3](https://github.com/maxesisnclaw/nexus/commit/e2db3a350903e94207112f5e9d3d48ed35fd33e4))
* **audit-r7:** M-1 docker stop state verification, M-2 clean Serve shutdown on Close ([0c8fa34](https://github.com/maxesisnclaw/nexus/commit/0c8fa343f6e609e42001825bc7bdaa092bddb269))
* **audit-v0.4-r1:** C-1 Python TCP loopback enforcement, C-2 inbound conn limit+timeout, M-1 control socket conn limit+deadline ([04741b3](https://github.com/maxesisnclaw/nexus/commit/04741b388ba2202107d030ebecd05c4489ce9ac2))
* **audit-v0.4-r2:** M-2 complete writes for length-prefix protocol, M-3 register failure propagation, M-4 trusted Noise keys ([9dde26f](https://github.com/maxesisnclaw/nexus/commit/9dde26f7e843c4de68286d02dfa73fe495256666))
* **audit-v0.4-r3:** m-1 round-robin offset cleanup, m-2 registry reaper WaitGroup, m-3 Python specific exceptions, m-4 duplicate service name check ([6acfb41](https://github.com/maxesisnclaw/nexus/commit/6acfb4180adc8af526d8de63b3788ac00d5badd5))
* **audit-v0.4-r4:** C-1 Python FD leak on multi-fd, C-2 reject non-loopback TCP calls, M-1 remote endpoint routing, M-2 watch conn exempt from read timeout ([be6cfb2](https://github.com/maxesisnclaw/nexus/commit/be6cfb242496da01167a6a21aaec49a8fff22a38))
* **audit-v0.4-r5:** M-3 heartbeat re-register, M-4 registry client IO deadline, M-5 Python call timeout+retry ([14ee360](https://github.com/maxesisnclaw/nexus/commit/14ee360628b18ae404d1112bca1b2de8d9390cad))
* **audit-v0.4-r6:** M-1 heartbeat/Close race, M-2 watch ack deadline, M-3 Python empty host loopback, M-4 refuse UDS for remote ([96b572d](https://github.com/maxesisnclaw/nexus/commit/96b572d4916a4f57c1e47806c8cde3b8a3849618))
* **audit:** C-1 registry deep copy, C-2 UDS dial restriction, M-4 watch overflow signal ([667bf75](https://github.com/maxesisnclaw/nexus/commit/667bf75fc528ccaa50e7fddc93953dfb5d0f9300))
* **audit:** C-1 UDS permissions, C-2 shutdown race, M-1 rollback errors ([9fd6644](https://github.com/maxesisnclaw/nexus/commit/9fd664476f7f1cf473d83928c566dbf754400f5f))
* **audit:** C-3 SIGKILL orphan, M-2 States lock scope, M-3 watcher drop counter ([4ffe08e](https://github.com/maxesisnclaw/nexus/commit/4ffe08e72132ca4a953cbaa4e50f969d6f2574c9))
* **audit:** harden control and server write timeouts ([aa86cc5](https://github.com/maxesisnclaw/nexus/commit/aa86cc568d48cee2284c78386ee87f2d0f55d797))
* **audit:** harden status and registry client flows ([9a0dde2](https://github.com/maxesisnclaw/nexus/commit/9a0dde241e80fa3d5c56bc37b8b2648ea3183972))
* **audit:** M-1 buffered acceptCh, M-2 process group signals, M-3 rollback error aggregation ([69b2488](https://github.com/maxesisnclaw/nexus/commit/69b24882cb0ad7283163d0477abd99aa7c6e4a0e))
* **audit:** m-1 nil context in test, m-2 pin staticcheck v0.7.0 ([9ff70ab](https://github.com/maxesisnclaw/nexus/commit/9ff70ab75ee1410e684852e8cd62b17244b5930f))
* **audit:** M-4 Serve cleanup, M-5 CallContext API, M-6 line buffer cap ([ed3b5cb](https://github.com/maxesisnclaw/nexus/commit/ed3b5cb9f1f28d16b5ec5f8615f777c7accde5d7))
* **audit:** M-5 daemon nil defense, m-1 README URL, m-2 goreleaser owner, m-3 error context, m-4 CI integration race ([84a123e](https://github.com/maxesisnclaw/nexus/commit/84a123ed54d8ddc3228bf130fa6c84208d19ee4a))
* **audit:** tighten daemon and validation semantics ([9c0127f](https://github.com/maxesisnclaw/nexus/commit/9c0127f7df54221a13a571828978798457d51dd7))
* **audit:** tighten python endpoint trust and CI ([07c83a6](https://github.com/maxesisnclaw/nexus/commit/07c83a66bb2552603514952dc6af23d948833e60))
* **build:** align docker toolchain with go.mod ([750680e](https://github.com/maxesisnclaw/nexus/commit/750680e7eafc1c17c083fa5442957bf01c710b4a))
* **docs,sdk:** align reserved config and uds cleanup ([74d9ce0](https://github.com/maxesisnclaw/nexus/commit/74d9ce0e077ab69df010f49db0497e455a3a75d5))
* **M-2,M-4,M-8:** message size limit, docs-only DependsOn/HealthCheck, restart backoff ([8db9cd4](https://github.com/maxesisnclaw/nexus/commit/8db9cd4b8a5bbdd52b40810a0442d3ace5fedff8))
* **M-5,m-3,m-4,m-5,m-7:** process log isolation, docs, docker name sanitization, godoc ([3a996e7](https://github.com/maxesisnclaw/nexus/commit/3a996e723f3c49f674b29b917a65e01bd96e0dac))
* **m-6:** add comment explaining /tmp usage for test socket paths ([459485d](https://github.com/maxesisnclaw/nexus/commit/459485d493de273ebf963f3d7b6ba231b4b13aac))
* **module:** qualify import path ([30cfaff](https://github.com/maxesisnclaw/nexus/commit/30cfafff43ab4ee8dc7ccb2379154f30d29ae4ef))
* **python-sdk:** align rpc wire protocol with go ([eea1b94](https://github.com/maxesisnclaw/nexus/commit/eea1b94cf3ebd1e6437082b42e815b347dcb770a))
* **registry:** bound watcher delivery ([5fad812](https://github.com/maxesisnclaw/nexus/commit/5fad812044a16cdbb9dca632c42bb5de956ba7b1))
* **sdk,daemon:** honor dual listen and graceful stop ([92457cc](https://github.com/maxesisnclaw/nexus/commit/92457ccea7061b0951eb872d6fae75bfa91ef149))
* **sdk:** reuse rpc connections ([d8c2aa5](https://github.com/maxesisnclaw/nexus/commit/d8c2aa57a40b50bcd4b0bb1a4f1327ac9ebd410e))
* **transport,docs:** harden tcp exposure and socket cleanup ([4051a63](https://github.com/maxesisnclaw/nexus/commit/4051a631b8fb5a821883916551e26f6a265ab9dc))
* **transport:** harden fd ownership and cleanup ([63ba086](https://github.com/maxesisnclaw/nexus/commit/63ba08646a344e2fb087893976b6653c58209fb4))

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
