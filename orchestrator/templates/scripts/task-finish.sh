#!/usr/bin/env bash
# task-finish.sh <task-id> "<summary>" "<backend>/<model>"
#
# Called by the AGENT (inside the CLI) after the work is done. Marks the
# task as "done" and appends the summary as a comment.
#
# Requires: jq
set -euo pipefail

TASK_ID="${1:?task-finish.sh <task-id> \"<summary>\" \"<author>\"}"
SUMMARY="${2:?missing summary}"
AUTHOR="${3:-agent}"

TASKS_JSON="${PROJECT_ROOT:-.}/tasks.json"
NOW="$(date -u +%FT%TZ)"

# Write in place to preserve inode (symlink-safe).
tmp="$(mktemp)"
jq --arg id "$TASK_ID" --arg s "$SUMMARY" --arg a "$AUTHOR" --arg ts "$NOW" '
    (.tasks[] | select(.id == $id) | .status) = "done"
    | (.tasks[] | select(.id == $id) | .comments) += [
        {"author": $a, "body": $s, "at": $ts}
      ]
' "$TASKS_JSON" > "$tmp" && cat "$tmp" > "$TASKS_JSON" && rm -f "$tmp"
