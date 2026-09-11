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

# Compares the Go and Python binaries on testdata/parity-project. Wired up
# in G1.5 once cli/status and cli/tasks exist; until then it's a no-op so
# `make lint test parity` is always a safe smoke sequence to run.
parity:
	@echo "parity: not implemented yet (G1.5)"

clean:
	rm -rf bin/ coverage.out
