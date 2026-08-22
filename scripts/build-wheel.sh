#!/usr/bin/env bash
# One-shot: build the SPA, embed it in the package, and build the wheel.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

bash scripts/build-spa.sh

# Ensure the build tool is installed in an isolated venv to avoid
# polluting the current env.
python3 -m pip install --quiet --upgrade build >/dev/null
python3 -m build --wheel --outdir dist

echo ""
echo "Wheel built:"
ls -la dist/*.whl | tail -1
