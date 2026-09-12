# `internal/notify` fixtures

Every file here came out of **running Python**, not out of reading it. The
payload capture in particular: webhook bodies are the kind of thing that looks
obvious and is wrong in a detail nobody checks, so they were recorded off the
wire.

## `python-payloads.json` — captured off the wire

A local `http.server` stood in for Slack and Discord while
`orchestrator/notifications.py`'s `Notifier` posted to it, recording every
request's path, `Content-Type` and raw body. Ten POSTs: five calls across two
channels, in this order.

```python
n = Notifier(slack_webhook=f"http://127.0.0.1:{port}/slack/hook",
             discord_webhook=f"http://127.0.0.1:{port}/discord/hook",
             timeout_s=5)
n.notify_blocked("F1.T3", reason="spec ambiguous: two readings of the acceptance criteria\nsecond line is dropped")
n.notify_ci_blocked("F1.T3", pr_url="https://github.com/o/r/pull/42", attempts=3)
n.notify_test()
n.notify_test("a custom message")
n.notify_blocked("F2.T1")        # no reason at all
```

The recorder answered the way the real services do — Slack 200 with the body
`ok`, Discord 204 — because the Go side treats any 2xx as an accept and that
should be tested against both shapes rather than against one invented one.

`notify_test.go` compares the **decoded message** against these, not the raw
body, and that is deliberate. Python's `json.dumps` defaults put a space after
the colon and escape non-ASCII (`—` where Go writes a literal em dash);
Go's encoder does neither. Both are the same JSON to Slack and to Discord, so
pinning the whitespace would pin `json.dumps`'s defaults rather than anything a
human ever sees. The message text is the contract, and it is compared byte for
byte.

Two things the capture settled that reading would not have:

- the reason is cut at its **first line**, so a multi-line block reason loses
  everything after the newline — the second line is in the input above for
  exactly that reason;
- an absent reason renders as the literal `unknown`, not as an empty tail.

## `digest-*.txt` — Python's own output

`digest_text` is pure, so these are just its return value for three inputs,
written to disk:

| File | Input |
|---|---|
| `digest-full.txt` | a two-line summary and three milestones: one complete, one with no name (so the id shows) and no ETA, one with no progress at all |
| `digest-no-milestones.txt` | the same summary, no milestones |
| `digest-empty.txt` | `digest_text({})` — nothing at all |

These are compared **byte for byte**: the digest is plain text, so there is no
encoder in between and nothing to reconcile. `digest-empty.txt` is a single
newline rather than an empty file, which is what `.strip() + "\n"` produces and
is easy to get wrong in either direction.
