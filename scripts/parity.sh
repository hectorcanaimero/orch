#!/usr/bin/env bash
# scripts/parity.sh — diff the Python and Go `orch` CLIs' --json output
# over the same real project (testdata/parity-project/): `status`, `tasks`,
# a filtered `tasks --status`, `events`, and `validate`. This is the paridad
# gate from the "Orch en Go" migration plan (ADR-G6): while it stays green,
# the Go rewrite hasn't drifted from the Python behavior it is replacing.
# See testdata/parity-project/README.md for how the fixture was built and
# why --project-id is pinned explicitly. `orch logs` has no --json and
# isn't compared here — see its golden under testdata/parity-project/
# goldens/. `orch graph` isn't either: it deliberately emits Graphviz DOT
# where Python emits an HTML+SVG page, so there is nothing to diff (see
# docs/brainstorm/go-migration-notes.md).
#
# Usage:
#   make build && scripts/parity.sh
#   PY_ORCH=/path/to/orch GO_BIN=/path/to/bin/orch scripts/parity.sh
#
# Exits 0 only when every compared command's stdout is identical after
# normalizing the fixture copy's absolute path. Exits 1 if either binary
# is missing, or on any mismatch (full diff printed per command).
set -uo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root" || exit 1

GO_BIN="${GO_BIN:-$repo_root/bin/orch}"
PY_ORCH="${PY_ORCH:-$repo_root/.venv/bin/orch}"

if [ ! -x "$PY_ORCH" ]; then
  echo "parity.sh: no Python orch at $PY_ORCH" >&2
  echo "  set PY_ORCH, or create one: python3 -m venv .venv && .venv/bin/pip install -e \".[dev]\"" >&2
  exit 1
fi
if [ ! -x "$GO_BIN" ]; then
  echo "parity.sh: $GO_BIN not found — run 'make build' first (or set GO_BIN)" >&2
  exit 1
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

project="$work/parity-project"
mkdir -p "$project"
cp -R "$repo_root/testdata/parity-project/." "$project"
# The checked-in goldens/README aren't part of a real project tree.
rm -rf "${project:?}/goldens" "$project/README.md"

project_id="parity-project"
fail=0

# normalize replaces this run's real (temp) path with the same
# "<PROJECT_ROOT>" placeholder testdata/parity-project/goldens/*.json use,
# so a diff isn't just mktemp noise.
normalize() { sed "s#$project#<PROJECT_ROOT>#g"; }

# compare LABEL ARGS... — runs both binaries with ARGS plus the pinned
# --project-root/--project-id, normalizes each stdout, and diffs them.
# stderr is captured separately and only shown on mismatch, since a
# command that's still a Go stub prints its "not implemented yet" (or a
# cobra usage error for a flag the stub doesn't know yet) there.
compare() {
  local label="$1"; shift
  local py_out go_out py_rc go_rc py_err go_err

  py_out="$("$PY_ORCH" "$@" --project-root "$project" --project-id "$project_id" \
    2>"$work/py.stderr")"
  py_rc=$?
  go_out="$("$GO_BIN" "$@" --project-root "$project" --project-id "$project_id" \
    2>"$work/go.stderr")"
  go_rc=$?
  py_err="$(cat "$work/py.stderr")"
  go_err="$(cat "$work/go.stderr")"

  py_out="$(printf '%s' "$py_out" | normalize)"
  go_out="$(printf '%s' "$go_out" | normalize)"

  # Both sides must actually SUCCEED before comparing output — two
  # commands that fail for unrelated reasons can still exit with the same
  # code and print nothing to stdout, which would otherwise read as a
  # match. (This bit a first draft of this script: an incomplete fixture
  # made Python fail validation while Go rejected an unrecognized flag on
  # its not-yet-implemented stub — same exit code, same empty stdout,
  # "ok" for entirely the wrong reason.)
  if [ "$py_rc" -ne 0 ] || [ "$go_rc" -ne 0 ]; then
    echo "FAIL $label (python exit=$py_rc, go exit=$go_rc — expected both 0)"
    [ -n "$py_err" ] && echo "  python stderr: $py_err"
    [ -n "$go_err" ] && echo "  go stderr: $go_err"
    fail=1
    return 1
  fi

  if [ "$py_out" = "$go_out" ]; then
    echo "ok   $label"
    return 0
  fi

  echo "FAIL $label (both exited 0, output differs)"
  diff -u <(printf '%s\n' "$py_out") <(printf '%s\n' "$go_out")
  fail=1
}

compare "status --json" status --json
compare "tasks --json" tasks --json
compare "tasks --status todo,in-progress --json" tasks --status todo,in-progress --json
compare "events F2.T3 --json" events F2.T3 --json
compare "validate --json" validate --json

exit "$fail"
