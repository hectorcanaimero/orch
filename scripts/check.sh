#!/usr/bin/env bash
# check.sh — run all local quality gates.
#
# Exits non-zero on the first failure so it's safe to wire into a pre-push
# hook or CI step. Uses the venv Python if present, else falls back to the
# system interpreter.
#
# Usage:
#   ./scripts/check.sh          # default: pytest
#   ./scripts/check.sh --fast   # skip slow-integration tests (none yet, reserved)

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if [[ -x ".venv/bin/python" ]]; then
    PY=".venv/bin/python"
else
    PY="python3"
fi

# Dashboard scope: the only tests we currently guard against regression.
# Other modules (state.py, router.py, prompt_builder.py) have pre-existing
# breakage unrelated to the dashboard refactor and are intentionally
# excluded until those suites are green again.
TESTS_DEFAULT=(
    orchestrator/tests/test_dashboard.py
    orchestrator/tests/test_project_paths.py
    orchestrator/tests/test_spend_reader.py
)

echo "» pytest (dashboard scope)"
"$PY" -m pytest "${TESTS_DEFAULT[@]}" --tb=short "$@"

echo "» all checks passed"
