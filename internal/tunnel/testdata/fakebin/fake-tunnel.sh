#!/bin/sh
# Fake tunnel-provider CLI for the tunnel and dashboard tests — stands in
# for cloudflared. Prints FAKE_TUNNEL_LINES (newline-separated, so a
# caller can include a URL line), then
# either exits with FAKE_TUNNEL_EXIT or sleeps until signaled (the normal
# "tunnel is up" case). SIGTERM is trapped and re-raised on self so the
# shell's own default handling still terminates the process — the point is
# only to prove the signal reached this process (Setpgid delivery), not to
# change what it does with it.
trap 'exit 0' TERM

# `cloudflared --version`, which the capability check runs.
if [ "${1:-}" = "--version" ]; then
  echo "cloudflared version 0.0.0-fake"
  exit 0
fi

if [ -n "${FAKE_TUNNEL_LINES:-}" ]; then
  printf '%b' "$FAKE_TUNNEL_LINES"
fi

# FAKE_TUNNEL_LATE_LINES arrive 1.5s later, the way cloudflared's URL does.
if [ -n "${FAKE_TUNNEL_LATE_LINES:-}" ]; then
  sleep 1.5
  printf '%b' "$FAKE_TUNNEL_LATE_LINES"
fi

if [ -n "${FAKE_TUNNEL_EXIT:-}" ]; then
  exit "$FAKE_TUNNEL_EXIT"
fi

while true; do
  sleep 0.1
done
