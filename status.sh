#!/usr/bin/env bash
# Live activity summary for currently-running orchestrator tasks.
# Usage: ./orchestrator/status.sh [events-file]
set -u
EV="${1:-$(ls -t /Volumes/PortableSSD/rupies/v2/orchestrator/state/events-*.jsonl 2>/dev/null | head -1)}"
[[ -z "${EV:-}" ]] && { echo "no events file"; exit 1; }
echo "==== ORCH STATUS $(date '+%H:%M:%S') ===="
echo "events: $EV"
echo
echo "-- last 5 events --"
tail -5 "$EV" | while read -r line; do
  echo "$line" | jq -r '"\(.ts | .[11:19])  \(.event_type|ascii_upcase)  \(.task_id)  \(.backend)  \(.extra.reason // .extra.cli_model // .extra.cost_usd // "")"' 2>/dev/null
done
echo
echo "-- alive dispatches (from tasks.json in-progress) --"
jq -r '.tasks[] | select(.status == "in-progress") | "  " + .id + "  [" + .model + "]"' /Volumes/PortableSSD/rupies/v2/tasks.json
echo
for tid in $(jq -r '.tasks[] | select(.status == "in-progress") | .id' /Volumes/PortableSSD/rupies/v2/tasks.json); do
  LOG="/Volumes/PortableSSD/rupies/v2/orchestrator/state/logs/${tid}.log"
  [[ ! -f "$LOG" ]] && continue
  echo "-- $tid recent activity (last 3 commands/tools) --"
  tail -c 15000 "$LOG" 2>/dev/null | grep -oE '"command":"[^"]{0,120}"|"tool":"[^"]*"|"text":"[^"]{0,120}"' | tail -3 | sed 's/^/  /'
  echo
done
echo "==== end ===="
