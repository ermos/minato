VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/ermos/minato/internal/cli.Version=$(VERSION)

.PHONY: build build-linux test lint fmt

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/minato ./cmd/minato

build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/minato-linux-amd64 ./cmd/minato
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/minato-linux-arm64 ./cmd/minato

test:
	go test ./...

lint:
	golangci-lint run ./...

fmt:
	gofmt -w .
