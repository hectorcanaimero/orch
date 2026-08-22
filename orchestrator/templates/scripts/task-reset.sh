#!/usr/bin/env bash
# task-reset.sh <task-id> [--author "<who>"] [--project-root PATH]
#
# Called by orch during startup reconcile (Issue #7) when a task is stuck in
# "in-progress" but its recorded PID is dead. Reverts the task to "todo" and
# appends an audit comment. Never touches other fields; safe to call multiple
# times (idempotent — reset of a `todo` task is a no-op).
#
# Requires: jq
set -euo pipefail

TASK_ID="${1:?task-reset.sh <task-id> …}"
shift || true

AUTHOR="orch-reconcile"
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

# Write in place to preserve inode (symlink-safe). Only mutates when the
# current status is "in-progress" — reset of any other status is a no-op.
tmp="$(mktemp)"
jq --arg id "$TASK_ID" --arg a "$AUTHOR" --arg ts "$NOW" '
    (.tasks[] | select(.id == $id and .status == "in-progress") | .status) = "todo"
    | (.tasks[] | select(.id == $id and .status == "todo") | .comments) += [
        {"author": $a, "body": "reset from in-progress (orphaned)", "at": $ts}
      ]
' "$TASKS_JSON" > "$tmp" && cat "$tmp" > "$TASKS_JSON" && rm -f "$tmp"
