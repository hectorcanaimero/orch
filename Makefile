BINARY := bin/orch
VERSION := $(shell git describe --tags --always)

.PHONY: build web test lint clean site-templates site-wasm

build: web
	go build -ldflags="-s -w -X main.version=$(VERSION)" -o $(BINARY) ./cmd/orch

# Builds BOTH bundles web/ produces, because both are embedded in the
# binary and neither is optional:
#
#   pnpm build             -> internal/dashboard/dist/build     (web/vite.config.ts)
#   pnpm build:stakeholder -> internal/publish/dist/stakeholder (web/vite.stakeholder.config.ts)
#
# They are separate vite configs, not two entries of one build, because
# they need incompatible `base` values — the dashboard is served at `/` by
# a real server, the stakeholder page is read off disk with relative
# paths. So they are two commands here too.
#
# `build` depends on this so `make build` always ships real pages, not
# just the placeholder dist/README.md files keep it compiling with;
# `test` deliberately does NOT — TestSPARequiresABuild
# (internal/dashboard/spa_test.go) and TestBundleRequiresABuild
# (internal/publish/bundle_test.go) give a clear "run pnpm build in web/"
# failure for a bare `make test`/`go test ./...` instead of silently
# building the SPA for you every run.
web:
	cd web && pnpm install --frozen-lockfile && pnpm build && pnpm build:stakeholder

test:
	go test ./... -race -cover

lint:
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "golangci-lint not installed locally; falling back to go vet (CI runs the real linter)"; \
		go vet ./...; \
	fi
	@if [ -d web/node_modules ]; then cd web && pnpm lint; else echo "web/node_modules missing; skipping web lint (run make web)"; fi

clean:
	rm -rf bin/ coverage.out

# The site's template gallery is committed HTML (site/ has no build);
# TestTemplateGalleryIsCurrent fails when it falls behind internal/templates.
site-templates:
	go run ./cmd/sitegen

# The playground's engine: atomize + graph compiled to WebAssembly. Built at
# deploy time by .github/workflows/pages.yml and gitignored, never committed.
site-wasm:
	GOOS=js GOARCH=wasm go build -trimpath -ldflags="-s -w" -o site/playground/orch.wasm ./cmd/orch-wasm
	cp "$$(go env GOROOT)/lib/wasm/wasm_exec.js" site/playground/wasm_exec.js
