#!/bin/sh
# Generic fake CLI for internal/doctor's backend tests, installed under
# several names (claude/codex/opencode) via symlinks or copies. Behavior is
# driven by env vars prefixed with the uppercased binary name — e.g. for
# `opencode`: FAKE_OPENCODE_VERSION_STDOUT/_EXIT, FAKE_OPENCODE_AUTH_EXIT.
name=$(basename "$0")
upper=$(echo "$name" | tr '[:lower:]' '[:upper:]')

case "$1" in
  --version)
    eval "exit_code=\${FAKE_${upper}_VERSION_EXIT:-0}"
    eval "stdout=\${FAKE_${upper}_VERSION_STDOUT:-\"$name version 1.0.0\"}"
    printf '%s\n' "$stdout"
    exit "$exit_code"
    ;;
  auth)
    if [ "$2" = "list" ]; then
      eval "exit_code=\${FAKE_${upper}_AUTH_EXIT:-0}"
      eval "stdout=\${FAKE_${upper}_AUTH_STDOUT:-}"
      eval "stderr=\${FAKE_${upper}_AUTH_STDERR:-}"
      [ -n "$stdout" ] && printf '%s\n' "$stdout"
      [ -n "$stderr" ] && printf '%s\n' "$stderr" >&2
      exit "$exit_code"
    fi
    echo "fake $name: unhandled auth subcommand: $*" >&2
    exit 127
    ;;
  *)
    echo "fake $name: unhandled args: $*" >&2
    exit 127
    ;;
esac
