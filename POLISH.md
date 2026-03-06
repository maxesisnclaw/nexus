# Nexus Polish Mission

## Your Role

You are a senior open-source maintainer taking over an early-stage project and polishing it to production-grade, 50k-star quality. You have full autonomy — no human approval needed. Iterate until YOU believe it's genuinely excellent.

## Current State

- 25 Go files, ~2400 LOC across 4 phases (daemon, transport, registry, SDK)
- Basic structure works: builds, vets, has some unit tests
- Quality level: "intern's first draft" — functional but far from production

## Target Quality: 50k-star Open Source Project

Think of projects like `containerd`, `etcd`, `traefik`, `caddy`. They share:

### Code Quality
- Clean, idiomatic Go — every function is obvious in purpose
- Comprehensive error handling with wrapped errors and context
- No magic, no clever tricks — boring code that works
- Race-condition-free concurrent code (verify with `go test -race`)
- Zero `go vet` / `staticcheck` warnings

### Testing
- Unit tests for every package with meaningful coverage (>80%)
- Integration tests that spin up real components and verify end-to-end flows
- **Docker-based integration tests**: This project targets Linux. You are on macOS. Use Docker containers to run Linux-specific integration tests (memfd, UDS SCM_RIGHTS, full daemon lifecycle). Build a test container image with Go toolchain and run tests inside it.
- Table-driven tests, clear test names, helpful failure messages
- Benchmark tests for performance-critical paths (transport, msgpack, memfd)
- `go test -race ./...` must pass

### Documentation
- **README.md**: Professional, with badges, architecture diagram (ASCII art), quick start, installation, configuration reference, SDK examples, contributing guide
- **GoDoc comments**: Every exported type, function, method, const has a clear doc comment
- **Examples**: `examples/` directory with runnable example services (at least 3: simple ping-pong, multi-service pipeline, Docker-based service)
- **CHANGELOG.md**: Proper versioning

### Project Infrastructure
- **Dockerfile**: Multi-stage build for nexusd
- **docker-compose.yml**: Example multi-service deployment
- **Makefile** (override AGENTS.md on this): build, test, lint, docker targets
- **CI config**: `.github/workflows/ci.yml` — build, test, lint, race detection
- **LICENSE**: MIT
- **.goreleaser.yml**: Release automation config

### API Design
- Review all exported APIs — are they minimal, consistent, well-named?
- Does the SDK feel natural to use? Would YOU enjoy using it?
- Are there sharp edges that would trip up a new user?

### Robustness
- Graceful shutdown everywhere
- Connection retry with backoff
- Proper resource cleanup (file descriptors, sockets, goroutines)
- Context propagation and cancellation
- Structured logging with appropriate levels

## How to Work

### Iteration Loop
1. **Audit**: Read all code, identify gaps against the target quality
2. **Plan**: Prioritize — fix critical issues first, then polish
3. **Implement**: Make changes, test as you go
4. **Verify**: `go build ./...`, `go vet ./...`, `go test ./...`, `go test -race ./...`
5. **Docker test**: Build a test container and run Linux-specific tests inside Docker
6. **Review**: Re-read your changes — would a 50k-star project accept this PR?
7. **Repeat**: Go back to step 1 until nothing meaningful is left to improve

### Docker Testing Setup
Since you're on macOS and Nexus is Linux-only for some features:
```bash
# Build a test image
docker build -t nexus-test -f Dockerfile.test .

# Run full test suite inside Linux container  
docker run --rm -v $(pwd):/workspace -w /workspace nexus-test go test -v -race ./...

# Run integration tests that need real UDS/memfd
docker run --rm --privileged -v $(pwd):/workspace -w /workspace nexus-test go test -v -tags integration ./...
```

Create `Dockerfile.test` yourself as part of the infrastructure work.

### Git Discipline
- Commit frequently with meaningful messages
- Group related changes into logical commits
- Don't squash everything into one giant commit

## Completion Criteria

Stop when ALL of these are true:
1. `go build ./...` — passes
2. `go vet ./...` — zero warnings  
3. `go test ./...` — all pass with >80% coverage
4. `go test -race ./...` — no races detected
5. Docker integration tests pass
6. README is professional and comprehensive
7. All exported APIs have GoDoc comments
8. Examples directory has runnable examples
9. You've done at least 3 full audit passes and found nothing significant to improve
10. You would genuinely recommend this project to a colleague

## What NOT to Do
- Don't add gRPC, protobuf, HTTP frameworks, etcd, consul
- Don't change the core architecture (UDS/TCP/msgpack/memfd)
- Don't add Kubernetes or container orchestration integration
- Don't over-engineer — simplicity is a feature
- Don't skip Docker testing just because you're on macOS
