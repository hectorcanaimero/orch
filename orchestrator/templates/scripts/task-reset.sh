#!/usr/bin/env bash
# task-reset.sh <task-id> [author] [note]
#
# Sprint B: forces a task back to `todo` via the active state backend.
# Only valid on the sqlite backend (file backend has no clean way to
# reopen a done task in tasks.json without hand-editing).
set -euo pipefail

TASK_ID="${1:?task-reset.sh <task-id> [author] [note]}"
AUTHOR="${2:-orch}"
NOTE="${3:-reset}"

PROJECT_ROOT="${PROJECT_ROOT:-.}"

exec orch task-status "$TASK_ID" todo \
    --author "$AUTHOR" \
    --note "$NOTE" \
    --project-root "$PROJECT_ROOT"
