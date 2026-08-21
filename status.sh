#!/usr/bin/env bash
# Live activity summary for a running orch instance.
#
# Usage:
#   ./status.sh                           # uses $ORCH_PROJECT_ROOT or CWD
#   ./status.sh /path/to/project          # explicit project root
#
# Prints the last 5 events from the most-recent events file, the currently
# in-progress task ids, and the last 3 commands each in-flight agent ran.
# Requires: jq.

set -u

PROJECT_ROOT="${1:-${ORCH_PROJECT_ROOT:-$PWD}}"
STATE_DIR="$PROJECT_ROOT/orchestrator/state"
TASKS_JSON="$PROJECT_ROOT/tasks.json"

if [[ ! -d "$STATE_DIR" ]]; then
    echo "state dir not found: $STATE_DIR" >&2
    echo "usage: $0 [PROJECT_ROOT]" >&2
    exit 1
fi
if ! command -v jq >/dev/null 2>&1; then
    echo "jq is required. Install with: brew install jq" >&2
    exit 1
fi

EV="$(ls -t "$STATE_DIR"/events-*.jsonl 2>/dev/null | head -1)"
if [[ -z "${EV:-}" ]]; then
    echo "no events file in $STATE_DIR"
    exit 1
fi

echo "==== ORCH STATUS $(date '+%H:%M:%S') ===="
echo "project: $PROJECT_ROOT"
echo "events:  $EV"
echo

echo "-- last 5 events --"
tail -5 "$EV" | while read -r line; do
    echo "$line" | jq -r '"\(.ts | .[11:19])  \(.event_type|ascii_upcase)  \(.task_id)  \(.backend)  \(.extra.reason // .extra.cli_model // .extra.cost_usd // "")"' 2>/dev/null
done
echo

if [[ ! -f "$TASKS_JSON" ]]; then
    echo "(no tasks.json at $TASKS_JSON — skipping in-flight summary)"
    echo "==== end ===="
    exit 0
fi

echo "-- alive dispatches (from tasks.json in-progress) --"
jq -r '.tasks[] | select(.status == "in-progress") | "  " + .id + "  [" + .model + "]"' "$TASKS_JSON"
echo

for tid in $(jq -r '.tasks[] | select(.status == "in-progress") | .id' "$TASKS_JSON"); do
    LOG="$STATE_DIR/logs/${tid}.log"
    [[ ! -f "$LOG" ]] && continue
    echo "-- $tid recent activity (last 3 commands/tools) --"
    tail -c 15000 "$LOG" 2>/dev/null | grep -oE '"command":"[^"]{0,120}"|"tool":"[^"]*"|"text":"[^"]{0,120}"' | tail -3 | sed 's/^/  /'
    echo
done
echo "==== end ===="
