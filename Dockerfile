# syntax=docker/dockerfile:1

FROM golang:1.26.0-alpine AS builder
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags "-s -w" -o /out/nexusd ./cmd/nexusd

FROM scratch
COPY --from=builder /out/nexusd /usr/local/bin/nexusd
ENTRYPOINT ["/usr/local/bin/nexusd"]
