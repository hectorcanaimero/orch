#!/usr/bin/env bash
# Builds the SPA and copies the output into orchestrator/spa/ so it
# ships with the wheel. Called by scripts/build-wheel.sh.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WEB_DIR="$REPO_ROOT/web"
DEST="$REPO_ROOT/orchestrator/spa"

if [ ! -d "$WEB_DIR" ]; then
  echo "error: $WEB_DIR not found" >&2
  exit 1
fi

cd "$WEB_DIR"

# Guard: VITE_API_BASE_URL must NOT be set at wheel-build time.
# If it is, Vite bakes it as a string literal and dead-code-eliminates the
# window.location.origin fallback — breaking multi-port and cross-origin use.
# Use web/.env.local.example as a reference for dev-only overrides.
if grep -q 'VITE_API_BASE_URL' "$WEB_DIR/.env" 2>/dev/null; then
  echo "error: web/.env contains VITE_API_BASE_URL — clear it before building the wheel." >&2
  echo "       Dev overrides belong in .env.local (gitignored), NOT .env." >&2
  exit 1
fi
if [ -n "${VITE_API_BASE_URL:-}" ]; then
  echo "error: VITE_API_BASE_URL is set in the shell environment — unset it before building the wheel." >&2
  exit 1
fi

# Prefer pnpm; fall back to npm if pnpm isn't on PATH.
if command -v pnpm >/dev/null 2>&1; then
  pnpm install --frozen-lockfile
  pnpm build
else
  echo "warning: pnpm not found; falling back to npm" >&2
  npm ci
  npm run build
fi

# `pnpm build`'s real output lands in internal/dashboard/dist/build/ (see
# web/vite.config.ts's build.outDir — G5.1, so the Go binary's
# `//go:embed` can reach it), not web/dist/. This script ships that same
# build inside the Python wheel too, so both binaries embed byte-identical
# SPA output from one build.
SPA_BUILD="$REPO_ROOT/internal/dashboard/dist/build"
if [ ! -d "$SPA_BUILD" ] || [ ! -f "$SPA_BUILD/index.html" ]; then
  echo "error: $SPA_BUILD (or its index.html) is missing after the build — check web/vite.config.ts's outDir." >&2
  exit 1
fi

rm -rf "$DEST"
cp -R "$SPA_BUILD" "$DEST"

# Marker file so operators (and orch itself) can tell "this SPA was
# shipped in the wheel" from a plain project-specific frontend/dist/ build
# (see orchestrator/dashboard/server.py's _resolve_spa_dist).
echo "{\"built_at\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\",\"source\":\"scripts/build-spa.sh\"}" > "$DEST/.orch-spa.json"

echo "SPA built and copied to $DEST"
