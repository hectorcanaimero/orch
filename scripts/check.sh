#!/usr/bin/env bash
# check.sh — run the full pytest suite.
#
# Exits non-zero on the first failure so it's safe to wire into a pre-push
# hook or CI step. Uses the venv Python if present, else falls back to the
# system interpreter.
#
# Usage:
#   ./scripts/check.sh              # full suite
#   ./scripts/check.sh -k budget    # forward pytest args (e.g. -k, -x, -vv)

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if [[ -x ".venv/bin/python" ]]; then
    PY=".venv/bin/python"
else
    PY="python3"
fi

echo "» pytest orchestrator/tests/"
"$PY" -m pytest orchestrator/tests/ "$@"

echo "» all checks passed"
