#!/bin/sh
# Fake tunnel-provider CLI for internal/tunnel's manager tests — stands in
# for autossh/bore. Prints FAKE_TUNNEL_LINES (newline-separated, so a
# caller can include a URL line and/or a reconnect-pattern line), then
# either exits with FAKE_TUNNEL_EXIT or sleeps until signaled (the normal
# "tunnel is up" case). SIGTERM is trapped and re-raised on self so the
# shell's own default handling still terminates the process — the point is
# only to prove the signal reached this process (Setpgid delivery), not to
# change what it does with it.
trap 'exit 0' TERM

if [ -n "${FAKE_TUNNEL_LINES:-}" ]; then
  printf '%b' "$FAKE_TUNNEL_LINES"
fi

if [ -n "${FAKE_TUNNEL_EXIT:-}" ]; then
  exit "$FAKE_TUNNEL_EXIT"
fi

while true; do
  sleep 0.1
done
