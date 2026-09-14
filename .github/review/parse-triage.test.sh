#!/usr/bin/env bash
#
# Fixture tests for parse-triage.py. Run anywhere:
#
#   bash .github/review/parse-triage.test.sh

set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
PARSE="$HERE/parse-triage.py"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

PASS=0
FAIL=0

# ok <name> <raw> <expected "priority type"> [comment-must-contain]
ok() {
  local name="$1" raw="$2" want="$3" needle="${4:-}"
  printf '%s' "$raw" > "$TMP/raw.txt"
  local got
  if ! got="$(python3 "$PARSE" --raw "$TMP/raw.txt" --out "$TMP/out.json" \
      --comment "$TMP/comment.md" --milestone v0.12.1 --owner maint 2>"$TMP/err")"; then
    echo "FAIL $name — expected a triage, got an error: $(cat "$TMP/err")"
    FAIL=$((FAIL + 1)); return
  fi
  if [ "$got" != "$want" ]; then
    echo "FAIL $name — expected '$want', got '$got'"
    FAIL=$((FAIL + 1)); return
  fi
  if [ -n "$needle" ] && ! grep -qF -- "$needle" "$TMP/comment.md"; then
    echo "FAIL $name — comment lacks '$needle'"
    FAIL=$((FAIL + 1)); return
  fi
  echo "ok   $name"; PASS=$((PASS + 1))
}

# rejects <name> <raw> [expected-exit]
rejects() {
  local name="$1" raw="$2" want_rc="${3:-1}"
  printf '%s' "$raw" > "$TMP/raw.txt"
  python3 "$PARSE" --raw "$TMP/raw.txt" --out "$TMP/out.json" >/dev/null 2>"$TMP/err"
  local rc=$?
  if [ "$rc" -ne "$want_rc" ]; then
    echo "FAIL $name — exit $rc, want $want_rc ($(head -c 80 "$TMP/err"))"
    FAIL=$((FAIL + 1)); return
  fi
  echo "ok   $name (exit $rc)"; PASS=$((PASS + 1))
}

URGENT='{"priority":"urgent","type":"bug","severity":"critical","summary":"Agents write to a second database.","reasoning":"State is split and no workaround works.","workaround":"","areas":["internal/config"]}'
PLANNED='{"priority":"Planned","type":"feature","severity":"low","summary":"Add a flag.","reasoning":"Nothing is broken.","workaround":"edit the file by hand"}'

ok "bare urgent JSON" "$URGENT" "urgent bug" "cc @maint"
ok "planned, enum case-insensitive" "$PLANNED" "planned feature" "edit the file by hand"
ok "fenced" $'Here you go:\n```json\n'"$URGENT"$'\n```' "urgent bug" "internal/config"
ok "CLI envelope with a response" "{\"session_id\":\"s\",\"response\":$(printf '%s' "$URGENT" | python3 -c 'import json,sys;print(json.dumps(sys.stdin.read()))')}" "urgent bug" "v0.12.1"

rejects "priority outside the enum" '{"priority":"asap","type":"bug","severity":"high","summary":"s","reasoning":"r"}'
rejects "missing reasoning" '{"priority":"planned","type":"bug","severity":"low","summary":"s"}'
rejects "areas not a list" '{"priority":"planned","type":"bug","severity":"low","summary":"s","reasoning":"r","areas":"internal/engine"}'
rejects "empty output" "" 2
rejects "empty CLI envelope" '{"session_id":"s","response":"","stats":{}}' 2

# A planned triage must not ping anyone.
printf '%s' "$PLANNED" > "$TMP/raw.txt"
python3 "$PARSE" --raw "$TMP/raw.txt" --out "$TMP/out.json" --comment "$TMP/comment.md" --owner maint >/dev/null 2>&1
if grep -q "cc @" "$TMP/comment.md"; then
  echo "FAIL planned pings the owner"; FAIL=$((FAIL + 1))
else
  echo "ok   planned does not ping"; PASS=$((PASS + 1))
fi

echo
echo "$PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
