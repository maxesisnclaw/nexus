# Nexus Examples

This directory contains three runnable examples:

1. `ping-pong`: single request/response flow between two SDK clients.
2. `pipeline`: two services composed as a multi-stage pipeline.
3. `docker-service`: container-friendly service binary and Dockerfile.

## Run Ping-Pong

```bash
go run ./examples/ping-pong
```

Expected output:

```text
ping-pong response: pong:hello
```

## Run Pipeline

```bash
go run ./examples/pipeline
```

Expected output:

```text
pipeline result: [pipeline] nexus pipeline
```

## Build Docker Service Image

```bash
docker build -t nexus-example-docker-service ./examples/docker-service
```

Run it with a mounted Nexus socket directory:

```bash
docker run --rm -v /run/nexus:/run/nexus nexus-example-docker-service
```
