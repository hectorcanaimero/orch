BINARY := bin/orch
VERSION := $(shell git describe --tags --always)

.PHONY: build test lint parity clean

build:
	go build -ldflags="-s -w -X main.version=$(VERSION)" -o $(BINARY) ./cmd/orch

test:
	go test ./... -race -cover

lint:
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "golangci-lint not installed locally; falling back to go vet (CI runs the real linter)"; \
		go vet ./...; \
	fi

# Compares the Go and Python binaries' --json output on
# testdata/parity-project (see its README.md). Needs `make build` first and
# a Python venv at .venv (python3 -m venv .venv && .venv/bin/pip install
# -e ".[dev]") — scripts/parity.sh explains how to point it elsewhere.
# Currently fails: internal/cli's status/tasks commands don't exist yet.
parity:
	scripts/parity.sh

clean:
	rm -rf bin/ coverage.out
