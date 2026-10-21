# BlessForge (bfctl) build automation.
BINARY  := bfctl
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

.PHONY: build test vet lint verify release clean

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/$(BINARY)

test:
	go test ./...

vet:
	go vet ./...

lint:
	golangci-lint run ./...

verify: vet lint test

release: build
	mkdir -p dist
	tar -czf dist/$(BINARY)-$(VERSION)-$$(go env GOOS)-$$(go env GOARCH).tar.gz -C bin $(BINARY)

clean:
	rm -rf bin dist
