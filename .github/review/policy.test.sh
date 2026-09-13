#!/usr/bin/env bash
#
# Fixture tests for policy.sh. No network, no GitHub — run it anywhere:
#
#   bash .github/review/policy.test.sh
#
# It is also wired into the `policy` job so a change to the rules that breaks
# its own fixtures fails CI before it can mislabel a real PR.

set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
POLICY="$HERE/policy.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

PASS=0
FAIL=0

# run <name> <files-json> <labels-json> <expected-eligible> [expected-reason-substring]
run() {
  local name="$1" files="$2" labels="$3" want="$4" want_reason="${5:-}"
  printf '%s' "$files"  > "$TMP/files.json"
  printf '%s' "$labels" > "$TMP/labels.json"

  local out
  if ! out="$(bash "$POLICY" --files "$TMP/files.json" --labels "$TMP/labels.json" --policy "$HERE/policy.json" 2>&1)"; then
    echo "FAIL $name — policy.sh exited non-zero:"
    echo "$out" | sed 's/^/      /'
    FAIL=$((FAIL + 1))
    return
  fi

  local got
  got="$(printf '%s\n' "$out" | sed -n 's/^eligible=//p')"
  if [ "$got" != "$want" ]; then
    echo "FAIL $name — expected eligible=$want, got eligible=$got"
    printf '%s\n' "$out" | sed 's/^/      /'
    FAIL=$((FAIL + 1))
    return
  fi

  if [ -n "$want_reason" ] && ! printf '%s' "$out" | grep -qF -- "$want_reason"; then
    echo "FAIL $name — reason did not mention '$want_reason'"
    printf '%s\n' "$out" | sed 's/^/      /'
    FAIL=$((FAIL + 1))
    return
  fi

  echo "ok   $name"
  PASS=$((PASS + 1))
}

# ---- the two diffs the brief asks for -------------------------------------

run "small docs+code change is eligible" \
  '[{"path":"docs/MANUAL.en.md","additions":12,"deletions":3},
    {"path":"internal/cli/status.go","additions":40,"deletions":8},
    {"path":"internal/cli/status_test.go","additions":60,"deletions":0}]' \
  '[]' \
  true \
  "123 changed lines"

run "touching .github/ is not eligible" \
  '[{"path":".github/workflows/review.yml","additions":90,"deletions":0},
    {"path":"docs/CI-REVIEW.md","additions":50,"deletions":0}]' \
  '[]' \
  false \
  ".github/workflows/review.yml"

# ---- each rule on its own --------------------------------------------------

run "over the line limit" \
  '[{"path":"internal/cli/big.go","additions":300,"deletions":150}]' \
  '[]' \
  false \
  "450 changed lines is over the 400 line limit"

run "exactly at the line limit is still eligible" \
  '[{"path":"internal/cli/edge.go","additions":400,"deletions":0}]' \
  '[]' \
  true

run "needs-human label blocks" \
  '[{"path":"README.md","additions":1,"deletions":1}]' \
  '["needs-human"]' \
  false \
  "needs-human"

run "an unrelated label does not block" \
  '[{"path":"README.md","additions":1,"deletions":1}]' \
  '["documentation","good first issue"]' \
  true

# ---- protected path patterns ----------------------------------------------

run "go.mod is protected (exact match)" \
  '[{"path":"go.mod","additions":2,"deletions":1}]' '[]' false "go.mod"

run "internal/state/** is protected (globstar)" \
  '[{"path":"internal/state/migrations/003_pr.sql","additions":10,"deletions":0}]' \
  '[]' false "internal/state/migrations/003_pr.sql"

run "internal/dashboard/auth* is protected (prefix)" \
  '[{"path":"internal/dashboard/auth_middleware.go","additions":5,"deletions":2}]' \
  '[]' false "auth_middleware.go"

# The Python tree left main in G7.5; its protected paths left the policy with
# it, so a path under the old tree is an ordinary path again, not a zone.
run "the archived Python tree is no longer a protected zone" \
  '[{"path":"orchestrator/dashboard/middleware.py","additions":5,"deletions":2}]' \
  '[]' true

run "a neighbouring path is NOT protected" \
  '[{"path":"internal/dashboard/summary.go","additions":5,"deletions":2}]' \
  '[]' true

run "every failing rule is reported at once" \
  '[{"path":".github/workflows/ci.yml","additions":500,"deletions":0}]' \
  '["needs-human"]' \
  false \
  "over the 400 line limit"

# ---- degenerate inputs -----------------------------------------------------

run "an empty PR is eligible" '[]' '[]' true "0 changed lines"

run "missing additions/deletions count as zero" \
  '[{"path":"README.md"}]' '[]' true "0 changed lines"

# ---------------------------------------------------------------------------

echo
echo "$PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
