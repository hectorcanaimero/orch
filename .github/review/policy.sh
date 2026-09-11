#!/usr/bin/env bash
#
# Decide whether a pull request is eligible for auto-merge.
#
# Pure function of its inputs: reads JSON, writes `key=value` lines to stdout.
# No network, no `gh`, no git. That is what makes it testable — see
# policy.test.sh, which drives it with fixture PRs.
#
# Usage:
#   policy.sh --files files.json --labels labels.json [--policy policy.json]
#
#   files.json   [{"path": "a.go", "additions": 3, "deletions": 1}, ...]
#                (the shape of `gh pr view --json files`)
#   labels.json  ["needs-human", ...]
#                (the shape of `gh pr view --json labels --jq '[.labels[].name]'`)
#
# Output (stdout, one per line — append it straight to $GITHUB_OUTPUT):
#   eligible=true|false
#   changed_lines=<int>
#   reason=<one line, human-readable>
#
# Exit status is 0 whenever the policy was evaluated, whatever the verdict.
# A non-zero exit means the inputs were unusable, which is a CI bug, not a
# "not eligible".

set -euo pipefail

die() { echo "policy.sh: $*" >&2; exit 2; }

FILES_JSON=""
LABELS_JSON=""
POLICY_JSON="$(dirname "$0")/policy.json"

while [ $# -gt 0 ]; do
  case "$1" in
    --files)  FILES_JSON="${2:-}"; shift 2 ;;
    --labels) LABELS_JSON="${2:-}"; shift 2 ;;
    --policy) POLICY_JSON="${2:-}"; shift 2 ;;
    *) die "unknown argument: $1" ;;
  esac
done

[ -n "$FILES_JSON" ]  || die "--files is required"
[ -n "$LABELS_JSON" ] || die "--labels is required"
[ -r "$FILES_JSON" ]  || die "cannot read $FILES_JSON"
[ -r "$LABELS_JSON" ] || die "cannot read $LABELS_JSON"
[ -r "$POLICY_JSON" ] || die "cannot read $POLICY_JSON"

command -v jq >/dev/null || die "jq is required"

MAX_LINES="$(jq -r '.max_changed_lines' "$POLICY_JSON")"
[ "$MAX_LINES" != "null" ] || die "policy is missing max_changed_lines"

# ---- rule 1: size ----------------------------------------------------------

CHANGED_LINES="$(jq '[.[] | (.additions // 0) + (.deletions // 0)] | add // 0' "$FILES_JSON")"

# ---- rule 2: protected paths ----------------------------------------------
#
# `**` and `*` both become a bash `*`, which matches `/` too. That makes a
# pattern slightly broader than a strict globstar would be — deliberately, so
# the failure mode is "a PR needs a human" rather than "a protected file
# merged itself".

HITS=()
while IFS= read -r pattern; do
  [ -n "$pattern" ] || continue
  glob="${pattern//\*\*/\*}"
  while IFS= read -r path; do
    [ -n "$path" ] || continue
    # shellcheck disable=SC2053 — $glob is a pattern on purpose.
    if [[ "$path" == $glob ]]; then
      HITS+=("$path")
    fi
  done < <(jq -r '.[].path' "$FILES_JSON")
done < <(jq -r '.protected_paths[]' "$POLICY_JSON")

# De-duplicate: one path can match several patterns.
PROTECTED_HITS=()
if [ ${#HITS[@]} -gt 0 ]; then
  while IFS= read -r p; do PROTECTED_HITS+=("$p"); done \
    < <(printf '%s\n' "${HITS[@]}" | sort -u)
fi

# ---- rule 3: blocking labels ----------------------------------------------

BLOCKING_LABELS=()
while IFS= read -r label; do
  [ -n "$label" ] || continue
  if jq -e --arg l "$label" 'index($l) != null' "$LABELS_JSON" >/dev/null; then
    BLOCKING_LABELS+=("$label")
  fi
done < <(jq -r '.blocking_labels[]?' "$POLICY_JSON")

# ---- verdict ---------------------------------------------------------------
#
# Every failing rule is reported, not just the first: a contributor should
# learn everything that needs fixing in one pass.

REASONS=()

if [ "$CHANGED_LINES" -gt "$MAX_LINES" ]; then
  REASONS+=("$CHANGED_LINES changed lines is over the $MAX_LINES line limit")
fi

if [ ${#PROTECTED_HITS[@]} -gt 0 ]; then
  shown="$(printf '%s, ' "${PROTECTED_HITS[@]:0:3}")"
  shown="${shown%, }"
  if [ ${#PROTECTED_HITS[@]} -gt 3 ]; then
    shown="$shown (+$(( ${#PROTECTED_HITS[@]} - 3 )) more)"
  fi
  REASONS+=("touches protected paths: $shown")
fi

if [ ${#BLOCKING_LABELS[@]} -gt 0 ]; then
  REASONS+=("labelled $(printf '%s ' "${BLOCKING_LABELS[@]}" | sed 's/ $//')")
fi

if [ ${#REASONS[@]} -eq 0 ]; then
  echo "eligible=true"
  echo "changed_lines=$CHANGED_LINES"
  echo "reason=$CHANGED_LINES changed lines, no protected paths, no blocking label"
else
  joined="$(printf '%s; ' "${REASONS[@]}")"
  echo "eligible=false"
  echo "changed_lines=$CHANGED_LINES"
  echo "reason=${joined%; }"
fi
