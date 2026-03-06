FROM golang:1.25.0-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/docker-service ./examples/docker-service

FROM scratch
COPY --from=builder /out/docker-service /usr/local/bin/docker-service
ENTRYPOINT ["/usr/local/bin/docker-service"]
