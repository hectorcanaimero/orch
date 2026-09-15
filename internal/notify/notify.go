// Package notify POSTs short messages to Slack and Discord webhooks.
//
// Port of orchestrator/notifications.py. Two properties are load-bearing and
// both come straight from that file's own reasoning:
//
//   - **Best effort, always.** A broken webhook must never surface into the
//     dispatch loop. The goal is "the operator does not have to babysit the
//     dashboard", not "add another way for a run to die". Every method except
//     Test swallows its errors.
//   - **Disabled is the default.** An empty URL disables that channel, and a
//     Notifier with no channels is a working no-op — safe to build and call
//     unconditionally from the engine, which is exactly how the engine uses
//     it.
//
// The event log remains the source of truth for what happened; these are a
// side channel.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// DefaultTimeout is Python's `notifications.timeout_s` default.
const DefaultTimeout = 5 * time.Second

// reasonMaxChars is how much of a block reason reaches the channel. Python
// takes the first line and truncates to 200 characters: a webhook message is
// a nudge, and a pasted traceback in a team channel is noise.
const reasonMaxChars = 200

// Notifier posts to whichever channels are configured.
//
// The zero value is a valid disabled notifier, so a caller that never
// configured one does not need a nil check.
type Notifier struct {
	SlackWebhook   string
	DiscordWebhook string
	Timeout        time.Duration

	// Client is the HTTP client. Nil uses a client with Timeout.
	Client *http.Client
	// Log receives the best-effort failures. Nil uses slog's default.
	Log *slog.Logger
}

// New builds a Notifier from the three config values.
func New(slackWebhook, discordWebhook string, timeoutSeconds float64) *Notifier {
	timeout := DefaultTimeout
	if timeoutSeconds > 0 {
		timeout = time.Duration(timeoutSeconds * float64(time.Second))
	}
	return &Notifier{
		SlackWebhook:   slackWebhook,
		DiscordWebhook: discordWebhook,
		Timeout:        timeout,
	}
}

// Enabled reports whether at least one channel is configured.
func (n *Notifier) Enabled() bool {
	if n == nil {
		return false
	}
	return n.SlackWebhook != "" || n.DiscordWebhook != ""
}

// Blocked announces a task the dispatch loop gave up on.
//
// Only the reason's first line is sent, truncated — see reasonMaxChars. An
// empty reason becomes "unknown", because a message that just says a task is
// blocked tells the reader nothing they can act on.
func (n *Notifier) Blocked(ctx context.Context, taskID, reason string) {
	n.send(ctx, fmt.Sprintf(":no_entry: orch: task `%s` blocked — %s",
		taskID, firstLine(reason)))
}

// CIBlocked announces a task blocked because CI kept failing.
func (n *Notifier) CIBlocked(ctx context.Context, taskID, prURL string, attempts int) {
	tail := ""
	if prURL != "" {
		tail = " (" + prURL + ")"
	}
	n.send(ctx, fmt.Sprintf(":warning: orch: task `%s` blocked after %d CI attempt(s)%s",
		taskID, attempts, tail))
}

// Stalled announces a run that has made no progress for stalledFor, and what
// it is waiting on, cut like a block reason. Go only: Python never noticed a
// stall (#255).
func (n *Notifier) Stalled(ctx context.Context, stalledFor time.Duration, waitingOn string) {
	n.send(ctx, fmt.Sprintf(":hourglass: orch: no progress for %s — waiting on %s",
		stalledFor, firstLine(waitingOn)))
}

// Test sends a one-off message and reports whether any channel accepted it.
//
// The one method that answers rather than swallowing: `orch notify test`
// exists so an operator finds out their webhook is wrong before a run depends
// on it, which needs a real exit code.
func (n *Notifier) Test(ctx context.Context, message string) bool {
	if message == "" {
		message = "orch: notifier test"
	}
	return n.send(ctx, message)
}

// firstLine is Python's `(reason or "unknown").strip().splitlines()[0][:200]`.
//
// The truncation counts characters, not bytes: Python slices a str, and a
// reason with an accent or an em dash in it would otherwise be cut mid-rune.
func firstLine(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "unknown"
	}
	if i := strings.IndexAny(reason, "\n\r"); i >= 0 {
		reason = reason[:i]
	}
	r := []rune(reason)
	if len(r) > reasonMaxChars {
		return string(r[:reasonMaxChars])
	}
	return reason
}

// payload is one channel's request: its URL and the body shape it wants.
//
// Slack takes {"text": ...}; Discord takes {"content": ...}.
type payload struct {
	url   string
	field string
}

func (n *Notifier) payloads() []payload {
	var out []payload
	if n.SlackWebhook != "" {
		out = append(out, payload{url: n.SlackWebhook, field: "text"})
	}
	if n.DiscordWebhook != "" {
		out = append(out, payload{url: n.DiscordWebhook, field: "content"})
	}
	return out
}

// send POSTs text to every configured channel and reports whether any
// accepted it. A notifier with no channels returns false without doing
// anything, which is what makes an unconfigured project free.
func (n *Notifier) send(ctx context.Context, text string) bool {
	if n == nil {
		return false
	}
	anyOK := false
	for _, p := range n.payloads() {
		if n.post(ctx, p, text) {
			anyOK = true
		}
	}
	return anyOK
}

func (n *Notifier) post(ctx context.Context, p payload, text string) bool {
	body, err := json.Marshal(map[string]string{p.field: text})
	if err != nil {
		// A map of strings cannot fail to marshal; this is here so the error
		// is not silently dropped rather than because it can happen.
		n.logger().Warn("notifier: could not encode the payload", "err", err)
		return false
	}

	timeout := n.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, p.url, bytes.NewReader(body))
	if err != nil {
		n.logger().Warn("notifier: bad webhook URL", "field", p.field, "err", err)
		return false
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.client(timeout).Do(req)
	if err != nil {
		// Best effort: the run carries on. The URL is not logged in full —
		// a webhook URL IS the credential, and a log line is a place it
		// should not end up.
		n.logger().Warn("notifier: POST failed", "channel", p.field, "err", err)
		return false
	}
	defer func() { _ = resp.Body.Close() }()

	// Slack answers 200 with the body "ok"; Discord answers 204. Any 2xx is
	// an accept.
	ok := resp.StatusCode >= 200 && resp.StatusCode < 300
	if !ok {
		n.logger().Warn("notifier: channel rejected the message",
			"channel", p.field, "status", resp.StatusCode)
	}
	return ok
}

func (n *Notifier) client(timeout time.Duration) *http.Client {
	if n.Client != nil {
		return n.Client
	}
	return &http.Client{Timeout: timeout}
}

func (n *Notifier) logger() *slog.Logger {
	if n.Log != nil {
		return n.Log
	}
	return slog.Default()
}
