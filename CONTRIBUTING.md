# Contributing to orch

orch is a single Go binary with a React SPA embedded in it. The Python
implementation it replaced lives on the `python-legacy` branch and is not
developed on `main`.

## Toolchain

- **Go** — the version in `go.mod`'s `go` directive is the minimum, and the only
  place that number lives.
- **pnpm** + **Node** — to build `web/`, which the binary embeds.
- **golangci-lint v2** built with a Go at least as new as `go.mod`'s (`make lint`
  falls back to `go vet` without it; CI runs the real one).

## Everyday commands

```bash
make web      # build both SPA bundles (dashboard + stakeholder) into the embed dirs
make test     # go test ./... -race -cover
make lint     # golangci-lint (or go vet with a warning), then oxlint over web/ once node_modules exists
make build    # bin/orch, version from git describe (runs make web first)
```

`go test ./...` without `make web` fails exactly two tests on purpose —
`TestSPARequiresABuild` and `TestBundleRequiresABuild` — each saying what to run.

## Pull requests

- Conventional commits (`feat:`, `fix:`, `test:`, `docs:`, `chore:`, `refactor:`).
- Every PR is reviewed by Gemini against `.github/review/CHECKLIST.md`; see
  [`docs/CI-REVIEW.md`](docs/CI-REVIEW.md) for what it checks and which paths
  always need a human.
- A new command, flag or config key comes with its row in
  [`docs/CLI.md`](docs/CLI.md) or [`docs/CONFIG.md`](docs/CONFIG.md).

## Releasing

A `v*` tag on `main` builds and publishes the binary through goreleaser — see
[`docs/RELEASING.md`](docs/RELEASING.md) for the tag scheme, the dry run every PR
already gets, and what the Homebrew step needs.
