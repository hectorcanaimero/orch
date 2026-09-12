#!/usr/bin/env bash
# scripts/parity.sh — the CI parity gate (G3.5): the same recorrido run
# through both the Python and Go `orch` binaries, diffed. While it stays
# green, the Go rewrite hasn't drifted from the Python behavior it is
# replacing (ADR-G6).
#
# Two kinds of check:
#
#   1. `compare` — read-only --json commands, diffed as text, over the
#      committed testdata/parity-project/ fixture: `status`, `tasks`, a
#      filtered `tasks --status`, `events`, `validate`.
#   2. `compare_tree` — a real workflow that WRITES files, diffed as a
#      directory tree: `orch init` (blank project) then `orch atomize
#      --apply` (a real spec) on a fresh project each binary scaffolds
#      itself. This is the part of the recorrido that actually exercises
#      `internal/model`'s tasks.json encoding end to end, which is how
#      #166's three bugs (JSON key order, description/model swapped,
#      estimateHours losing its decimal point) were found — by hand, before
#      this script covered it.
#
# Deliberately NOT compared here, each for a documented reason rather than
# a normalization that would quietly cover for it:
#
#   - `orch logs` has no --json — see its golden under
#     testdata/parity-project/goldens/ instead.
#   - `orch graph` emits Graphviz DOT; Python emits an HTML+SVG page. No
#     shared format exists to diff (docs/brainstorm/go-migration-notes.md).
#   - `orch run` (the dispatch loop) with `ORCH_FAKE_PROVIDER`: that env
#     var is Go-only (internal/engine) — Python's dispatch loop has no
#     equivalent fake-provider mode, so there is nothing on the Python side
#     to run without spawning a real, non-deterministic coding CLI in CI.
#   - `orch run --dry-run`: Python's bare `orch --dry-run --json` prints the
#     dispatch plan and exits; Go's `orch run` doesn't implement `--dry-run`
#     yet (docs/CLI.md's `run` row lists it under "flags deliberately not
#     registered"). Add it here once it exists on both sides.
#
# `model_router.yaml`'s stub header comment has different WORDING between
# the two binaries (cosmetic, not a difference in what the file configures)
# — see docs/brainstorm/go-migration-notes/sonnet.md. `compare_tree`
# excludes that one file from the raw tree diff and instead diffs it with
# comment/blank lines stripped, so a real route-entry divergence still
# fails loudly; the wording itself is not asserted on at all, deliberately,
# rather than normalized to look identical.
#
# Usage:
#   make build && scripts/parity.sh
#   PY_ORCH=/path/to/orch GO_BIN=/path/to/bin/orch scripts/parity.sh
#
# Exits 0 only when every check passes. Exits 1 if either binary is
# missing, or on any mismatch (full diff printed per check).
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

# router_content strips comments and blank lines from a project's
# model_router.yaml, leaving only whatever real route entries it has.
# See the header comment for why the file's wording itself is excluded.
router_content() {
  grep -vE '^[[:space:]]*(#|$)' "$1/.orchestrator/model_router.yaml" 2>/dev/null || true
}

# compare_tree LABEL PY_DIR GO_DIR — diffs two project directories one
# binary scaffolded/wrote independently (not copies of the same fixture,
# the way `compare`'s $project is): the whole tree except:
#   - model_router.yaml, compared separately below with comments stripped
#     (see the header comment for why its wording is excluded outright);
#   - .orchestrator/state/, which Python's `orch atomize --apply` populates
#     by best-effort syncing the parsed tasks into SQLite's
#     tasks_definition (state_backend.upsert_task_definition) and Go's does
#     not — state.Backend has no method shaped for that write yet, the
#     same documented gap `task set --model/--backend/--milestone` has.
#     tasks.json is authoritative on both sides either way; excluded here,
#     not silently made to match, because there is nothing to normalize —
#     Go genuinely does not write this directory during atomize;
#   - tasks.json.bak-<timestamp>, whose name is never going to match
#     between two runs a second or more apart. Its CONTENT isn't checked
#     either: it is a copy of the pre-apply tasks.json, already covered by
#     every other case in this workflow having compared that same file.
compare_tree() {
  local label="$1" py_dir="$2" go_dir="$3"
  local mismatch=0

  if ! diff -rq -x model_router.yaml -x state -x 'tasks.json.bak-*' \
      "$py_dir" "$go_dir" > "$work/tree.diff" 2>&1; then
    echo "FAIL $label (file tree differs)"
    cat "$work/tree.diff"
    mismatch=1
  fi

  if ! diff <(router_content "$py_dir") <(router_content "$go_dir") > "$work/router.diff" 2>&1; then
    echo "FAIL $label (model_router.yaml's real route entries differ)"
    cat "$work/router.diff"
    mismatch=1
  fi

  if [ "$mismatch" -eq 0 ]; then
    echo "ok   $label"
  else
    fail=1
  fi
}

# The init -> atomize workflow: each binary scaffolds its OWN project (not
# a copy of one fixture) under the same name, so any absolute-path leakage
# into a written file would show up as a real diff rather than being
# masked by both sides sharing one temp dir.
init_py="$work/workflow-py"
init_go="$work/workflow-go"
mkdir -p "$init_py" "$init_go"
workflow_name="parity-workflow"

# `orch init PATH` scaffolds relative to cwd — run each from its own workdir
# so PATH="$workflow_name" lands in the right place without fighting the
# other binary over the same directory.
(cd "$init_py" && "$PY_ORCH" init "$workflow_name") > "$work/py-init.out" 2>&1
py_init_rc=$?
(cd "$init_go" && "$GO_BIN" init "$workflow_name") > "$work/go-init.out" 2>&1
go_init_rc=$?
if [ "$py_init_rc" -ne 0 ] || [ "$go_init_rc" -ne 0 ]; then
  echo "FAIL init (python exit=$py_init_rc, go exit=$go_init_rc — expected both 0)"
  cat "$work/py-init.out" "$work/go-init.out"
  fail=1
else
  compare_tree "init (blank project)" "$init_py/$workflow_name" "$init_go/$workflow_name"

  # A real spec, written into both freshly-scaffolded projects, then
  # applied. This is what exercises tasks.json's actual field encoding —
  # init's own tasks.json has zero tasks, so it alone can't catch a
  # per-task bug like #166's.
  spec="specs/f0-foundation.md"
  cat > "$init_py/$workflow_name/$spec" <<'SPEC'
# F0 — Foundation

## F0.1 — Package: scaffolding

### F0.1.T1 — Setup monorepo

- **Modelo**: claude-sonnet-4-6
- **Estimación**: 1h
- **Razón**: Cheap boilerplate.
- **Files**:
  - `package.json`

### F0.1.T2 — Root README

- **Modelo**: claude-opus-4-7
- **Estimación**: 0.5h
- **Razón**: Short docs task.
- **Dependencies**: F0.1.T1
- **Files**:
  - `README.md`
SPEC
  cp "$init_py/$workflow_name/$spec" "$init_go/$workflow_name/$spec"

  py_atomize_rc=0
  go_atomize_rc=0
  (cd "$init_py/$workflow_name" && "$PY_ORCH" atomize --apply --project-root . \
    --project-id "$workflow_name") > "$work/py-atomize.out" 2>&1 || py_atomize_rc=$?
  (cd "$init_go/$workflow_name" && "$GO_BIN" atomize --apply --project-root . \
    --project-id "$workflow_name") > "$work/go-atomize.out" 2>&1 || go_atomize_rc=$?
  if [ "$py_atomize_rc" -ne 0 ] || [ "$go_atomize_rc" -ne 0 ]; then
    echo "FAIL atomize --apply (python exit=$py_atomize_rc, go exit=$go_atomize_rc — expected both 0)"
    cat "$work/py-atomize.out" "$work/go-atomize.out"
    fail=1
  else
    compare_tree "atomize --apply (real spec)" "$init_py/$workflow_name" "$init_go/$workflow_name"
  fi
fi

exit "$fail"
