GO ?= go
DOCKER ?= docker

.PHONY: build test race vet lint cover benchmark docker-build docker-test-image docker-test docker-integration ci

build:
	CGO_ENABLED=0 $(GO) build ./...

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

lint:
	@if command -v staticcheck >/dev/null 2>&1; then \
		staticcheck ./...; \
	else \
		echo "staticcheck not installed; running go vet instead"; \
		$(GO) vet ./...; \
	fi

cover:
	$(GO) test -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out | tail -n 1

benchmark:
	$(GO) test -bench=. -benchmem ./...

docker-build:
	$(DOCKER) build -t nexusd:local -f Dockerfile .

docker-test-image:
	$(DOCKER) build -t nexus-test -f Dockerfile.test .

docker-test: docker-test-image
	$(DOCKER) run --rm -v $$(pwd):/workspace -w /workspace nexus-test go test -v -race ./...

docker-integration: docker-test-image
	$(DOCKER) run --rm --privileged -v $$(pwd):/workspace -w /workspace nexus-test go test -v -tags integration ./...

ci: vet lint test race cover
