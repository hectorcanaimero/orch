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

# G3.5's CI gate: the same real workflow (status/tasks/events/validate
# --json, then init -> atomize --apply) run through both binaries and
# diffed — see scripts/parity.sh's own header for what's compared, what's
# deliberately excluded, and why. Needs `make build` first and a Python
# venv at .venv (python3 -m venv .venv && .venv/bin/pip install -e
# ".[dev]") — scripts/parity.sh explains how to point it elsewhere. Also
# runs in CI as the `parity` job in .github/workflows/go.yml.
parity:
	scripts/parity.sh

clean:
	rm -rf bin/ coverage.out
