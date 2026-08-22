#!/usr/bin/env bash
# Builds the SPA and copies the output into orchestrator/spa/ so it
# ships with the wheel. Called by scripts/build-wheel.sh.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
FRONTEND_DIR="$REPO_ROOT/frontend"
DEST="$REPO_ROOT/orchestrator/spa"

if [ ! -d "$FRONTEND_DIR" ]; then
  echo "error: $FRONTEND_DIR not found" >&2
  exit 1
fi

cd "$FRONTEND_DIR"

# Prefer pnpm; fall back to npm if pnpm isn't on PATH.
if command -v pnpm >/dev/null 2>&1; then
  pnpm install --frozen-lockfile
  pnpm build
else
  echo "warning: pnpm not found; falling back to npm" >&2
  npm ci
  npm run build
fi

rm -rf "$DEST"
cp -R "$FRONTEND_DIR/dist" "$DEST"

# Marker file so operators (and orch itself) can tell "this SPA was
# shipped in the wheel" from a plain frontend/dist/ build.
echo "{\"built_at\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\",\"source\":\"scripts/build-spa.sh\"}" > "$DEST/.orch-spa.json"

echo "SPA built and copied to $DEST"
