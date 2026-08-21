#!/usr/bin/env bash
# task-block.sh <task-id> "<reason>" "<backend>/<model>"
#
# Called by the AGENT when it can't proceed. Marks the task as "blocked"
# and appends the reason as a comment.
#
# Requires: jq
set -euo pipefail

TASK_ID="${1:?task-block.sh <task-id> \"<reason>\" \"<author>\"}"
REASON="${2:?missing reason}"
AUTHOR="${3:-agent}"

TASKS_JSON="${PROJECT_ROOT:-.}/tasks.json"
NOW="$(date -u +%FT%TZ)"

jq --arg id "$TASK_ID" --arg r "$REASON" --arg a "$AUTHOR" --arg ts "$NOW" '
    (.tasks[] | select(.id == $id) | .status) = "blocked"
    | (.tasks[] | select(.id == $id) | .comments) += [
        {"author": $a, "body": $r, "at": $ts}
      ]
' "$TASKS_JSON" > "$TASKS_JSON.tmp" && mv "$TASKS_JSON.tmp" "$TASKS_JSON"
