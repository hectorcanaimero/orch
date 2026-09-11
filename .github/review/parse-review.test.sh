#!/usr/bin/env bash
#
# Fixture tests for parse-review.py. Run anywhere:
#
#   bash .github/review/parse-review.test.sh
#
# Covers the envelopes a model actually produces (bare JSON, fenced, prose
# around it) and the malformed shapes that must NOT be allowed to turn into a
# green check.

set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
PARSE="$HERE/parse-review.py"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

PASS=0
FAIL=0

# ok <name> <raw> <expected-verdict>
ok() {
  local name="$1" raw="$2" want="$3"
  printf '%s' "$raw" > "$TMP/raw.txt"
  local got
  if ! got="$(python3 "$PARSE" --raw "$TMP/raw.txt" --out "$TMP/out.json" 2>"$TMP/err")"; then
    echo "FAIL $name — expected a verdict, got an error: $(cat "$TMP/err")"
    FAIL=$((FAIL + 1)); return
  fi
  if [ "$got" != "$want" ]; then
    echo "FAIL $name — expected verdict '$want', got '$got'"
    FAIL=$((FAIL + 1)); return
  fi
  echo "ok   $name"; PASS=$((PASS + 1))
}

# rejects <name> <raw>
rejects() {
  local name="$1" raw="$2"
  printf '%s' "$raw" > "$TMP/raw.txt"
  if python3 "$PARSE" --raw "$TMP/raw.txt" --out "$TMP/out.json" >/dev/null 2>"$TMP/err"; then
    echo "FAIL $name — parsed something it should have rejected"
    FAIL=$((FAIL + 1)); return
  fi
  echo "ok   $name (rejected: $(head -c 90 "$TMP/err"))"; PASS=$((PASS + 1))
}

GOOD='{"verdict":"approve","blocking":[],"minor":[],"summary":"Small doc change, nothing to flag."}'

ok "bare JSON" "$GOOD" approve

ok "fenced with a language tag" "$(printf '```json\n%s\n```' "$GOOD")" approve

ok "fenced without a language tag" "$(printf '```\n%s\n```' "$GOOD")" approve

ok "prose before and after" \
  "$(printf 'Here is my review:\n\n%s\n\nHope that helps!' "$GOOD")" approve

ok "request_changes with a blocking finding" \
  '{"verdict":"request_changes","blocking":[{"file":"internal/state/db.go","line":42,"rule":"one writer to SQLite","why":"opens a second writable *sql.DB"}],"minor":[],"summary":"One blocking issue."}' \
  request_changes

ok "comment verdict with only minor findings" \
  '{"verdict":"comment","blocking":[],"minor":[{"file":"a.go","rule":"table tests","why":"four near-identical test funcs"}],"summary":"Nothing blocking."}' \
  comment

# The verdict and the findings disagreeing is a model slip; the findings win,
# because they are the part a human can check.
ok "blocking findings force request_changes" \
  '{"verdict":"approve","blocking":[{"file":"go.mod","rule":"compatibility","why":"drops a dependency still imported"}],"minor":[],"summary":"Looks fine to me."}' \
  request_changes

ok "missing minor[] defaults to empty" \
  '{"verdict":"approve","blocking":[],"summary":"Fine."}' approve

# ---- must be rejected (→ neutral check, never success) --------------------

rejects "empty output"               ""
rejects "prose with no JSON at all"  "I reviewed the PR and it looks good to me!"
rejects "truncated JSON"             '{"verdict":"approve","blocking":[],"summary":'
rejects "verdict outside the enum"   '{"verdict":"lgtm","blocking":[],"minor":[],"summary":"ok"}'
rejects "empty summary"              '{"verdict":"approve","blocking":[],"minor":[],"summary":"  "}'
rejects "finding missing rule"       '{"verdict":"request_changes","blocking":[{"file":"a.go","why":"bad"}],"minor":[],"summary":"x"}'
rejects "finding missing file"       '{"verdict":"request_changes","blocking":[{"rule":"r","why":"bad"}],"minor":[],"summary":"x"}'
rejects "blocking is not a list"     '{"verdict":"approve","blocking":"none","minor":[],"summary":"x"}'
rejects "top level is a list"        '[{"verdict":"approve"}]'

# ---- the rendered comment --------------------------------------------------

printf '%s' '{"verdict":"request_changes","blocking":[{"file":"internal/engine/loop.go","line":88,"rule":"os/exec timeout","why":"no context deadline on the provider call"}],"minor":[{"file":"internal/cli/run.go","rule":"table tests","why":"three near-identical cases"}],"summary":"One blocking issue in the dispatch loop."}' > "$TMP/raw.txt"
python3 "$PARSE" --raw "$TMP/raw.txt" --out "$TMP/out.json" --comment "$TMP/c.md" \
  --eligible false --eligible-reason "touches protected paths: internal/engine/loop.go" \
  --run-url "https://example.invalid/run/1" >/dev/null

check_comment() {
  local what="$1" needle="$2"
  if grep -qF -- "$needle" "$TMP/c.md"; then
    echo "ok   comment $what"; PASS=$((PASS + 1))
  else
    echo "FAIL comment $what — missing '$needle'"; FAIL=$((FAIL + 1))
  fi
}

check_comment "carries the hidden marker"  "<!-- orch:gemini-review -->"
check_comment "shows the verdict"          "Changes requested"
check_comment "shows file:line"            "internal/engine/loop.go:88"
check_comment "has a Blocking section"     "**Blocking**"
check_comment "has a Minor section"        "**Minor**"
check_comment "reports ineligibility"      "Not auto-merge eligible"
check_comment "says nothing auto-merges"   "nothing merges automatically yet"
check_comment "links the run"              "https://example.invalid/run/1"

echo
echo "$PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
