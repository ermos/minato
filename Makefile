VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/ermos/minato/internal/cli.Version=$(VERSION)

.PHONY: build test lint fmt snapshot docs docs-serve

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/minato ./cmd/minato

test:
	go test -race ./...

lint:
	golangci-lint run ./...

fmt:
	gofmt -w .

# Release binaries for every platform in dist/, without publishing.
snapshot:
	goreleaser release --snapshot --clean

docs:
	cd website && npm ci && npm run build

docs-serve:
	cd website && npm start
