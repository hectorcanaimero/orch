BINARY := bin/orch
VERSION := $(shell git describe --tags --always)

.PHONY: build web test lint parity clean

build: web
	go build -ldflags="-s -w -X main.version=$(VERSION)" -o $(BINARY) ./cmd/orch

# Builds the SPA into internal/dashboard/dist/build (web/vite.config.ts's
# build.outDir — see internal/dashboard/spa.go) so `go:embed` has a real
# SPA, not just the placeholder dist/README.md keeps it compiling with.
# `build` depends on this so `make build` always ships a real dashboard;
# `test` deliberately does NOT — TestSPARequiresABuild
# (internal/dashboard/spa_test.go) gives a clear "run pnpm build in web/"
# failure for a bare `make test`/`go test ./...` instead of silently
# building the SPA for you every run.
web:
	cd web && pnpm install --frozen-lockfile && pnpm build

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
