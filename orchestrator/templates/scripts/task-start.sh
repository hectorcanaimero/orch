#!/usr/bin/env bash
# task-start.sh <task-id> [--author "<backend>/<model>"] [--project-root PATH]
#
# Called by orch RIGHT BEFORE spawning the agent CLI. Marks the task as
# "in-progress" and appends an audit comment. Never mutates other fields.
#
# Requires: jq
set -euo pipefail

TASK_ID="${1:?task-start.sh <task-id> …}"
shift || true

AUTHOR="orch"
PROJECT_ROOT="."
while [[ $# -gt 0 ]]; do
    case "$1" in
        --author)        AUTHOR="$2"; shift 2 ;;
        --project-root)  PROJECT_ROOT="$2"; shift 2 ;;
        *)               shift ;;
    esac
done

TASKS_JSON="$PROJECT_ROOT/tasks.json"
NOW="$(date -u +%FT%TZ)"

# Write in place to preserve inode (symlink-safe).
tmp="$(mktemp)"
jq --arg id "$TASK_ID" --arg a "$AUTHOR" --arg ts "$NOW" '
    (.tasks[] | select(.id == $id) | .status) = "in-progress"
    | (.tasks[] | select(.id == $id) | .comments) += [
        {"author": $a, "body": "started", "at": $ts}
      ]
' "$TASKS_JSON" > "$tmp" && cat "$tmp" > "$TASKS_JSON" && rm -f "$tmp"
