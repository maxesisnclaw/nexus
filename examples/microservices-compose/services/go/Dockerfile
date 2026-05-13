# Build the three demo Go services (gateway / orders / payment) in one image.
# Build context: repo root (so the local `replace` directive in go.mod can
# resolve ../../.. against the in-tree nexus source).
FROM golang:1.26.1-alpine AS builder

WORKDIR /workspace

# bring in the nexus source tree referenced by the local replace directive
COPY . /workspace/nexus

# demo module
WORKDIR /workspace/nexus/examples/microservices-compose/services/go
RUN go mod download || true

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w" \
      -o /out/gateway ./cmd/gateway && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w" \
      -o /out/orders ./cmd/orders && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w" \
      -o /out/payment ./cmd/payment

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tini
COPY --from=builder /out/gateway /usr/local/bin/gateway
COPY --from=builder /out/orders  /usr/local/bin/orders
COPY --from=builder /out/payment /usr/local/bin/payment
ENTRYPOINT ["/sbin/tini", "--"]
