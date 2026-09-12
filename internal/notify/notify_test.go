package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- The captured Python payloads ---------------------------------------

// capturedPOST is one request Python's Notifier really made, recorded by a
// local HTTP server. See testdata/README.md for how.
type capturedPOST struct {
	Path        string `json:"path"`
	ContentType string `json:"content_type"`
	Body        string `json:"body"`
}

func loadCaptured(t *testing.T) []capturedPOST {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "python-payloads.json")) // #nosec G304 -- a test-local literal
	if err != nil {
		t.Fatalf("reading the captured payloads: %v", err)
	}
	var out []capturedPOST
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("decoding the captured payloads: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("the capture is empty")
	}
	return out
}

// messageOf pulls the human-visible text out of a captured body, whichever
// field the channel uses.
func messageOf(t *testing.T, body string) string {
	t.Helper()
	var m map[string]string
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("decoding %q: %v", body, err)
	}
	for _, field := range []string{"text", "content"} {
		if v, ok := m[field]; ok {
			return v
		}
	}
	t.Fatalf("no text/content field in %q", body)
	return ""
}

// recorder is a webhook that records what it was sent.
type recorder struct {
	mu     sync.Mutex
	bodies []string
	types  []string
	// status is what it answers; 0 means 200 for slack and 204 for discord.
	status int
	srv    *httptest.Server
}

func newRecorder(t *testing.T, status int) *recorder {
	t.Helper()
	r := &recorder{status: status}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.bodies = append(r.bodies, string(body))
		r.types = append(r.types, req.Header.Get("Content-Type"))
		r.mu.Unlock()

		code := r.status
		if code == 0 {
			// Mirror the real services: Slack 200, Discord 204.
			code = http.StatusOK
			if strings.Contains(req.URL.Path, "discord") {
				code = http.StatusNoContent
			}
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *recorder) slackURL() string   { return r.srv.URL + "/slack/hook" }
func (r *recorder) discordURL() string { return r.srv.URL + "/discord/hook" }

func (r *recorder) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.bodies...)
}

func (r *recorder) contentTypes() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.types...)
}

// ---- Messages match what Python sent ------------------------------------

// TestMessagesMatchPython is the point of the captured fixture: the text that
// lands in someone's channel is compared against the text Python really sent,
// for the same inputs.
//
// The comparison is on the decoded message rather than on the raw body, and
// that is deliberate. Python's json.dumps defaults put a space after the
// colon and escape non-ASCII (`—` for the em dash); Go's encoder does
// neither. Both are the same JSON to Slack and to Discord, and pinning the
// whitespace would be pinning json.dumps's defaults rather than anything a
// human sees. The message itself is the contract, and it is compared byte for
// byte.
func TestMessagesMatchPython(t *testing.T) {
	captured := loadCaptured(t)

	// The capture was produced by this sequence; see testdata/README.md.
	calls := []struct {
		name string
		run  func(n *Notifier)
	}{
		{
			name: "blocked, with a multi-line reason",
			run: func(n *Notifier) {
				n.Blocked(context.Background(), "F1.T3",
					"spec ambiguous: two readings of the acceptance criteria\nsecond line is dropped")
			},
		},
		{
			name: "ci blocked",
			run: func(n *Notifier) {
				n.CIBlocked(context.Background(), "F1.T3", "https://github.com/o/r/pull/42", 3)
			},
		},
		{name: "the default test message", run: func(n *Notifier) { n.Test(context.Background(), "") }},
		{name: "a custom test message", run: func(n *Notifier) { n.Test(context.Background(), "a custom message") }},
		{name: "blocked with no reason", run: func(n *Notifier) { n.Blocked(context.Background(), "F2.T1", "") }},
	}

	if len(captured) != len(calls)*2 {
		t.Fatalf("the capture has %d POSTs for %d calls across 2 channels; it is out of step with this test",
			len(captured), len(calls))
	}

	for i, c := range calls {
		t.Run(c.name, func(t *testing.T) {
			rec := newRecorder(t, 0)
			n := &Notifier{
				SlackWebhook:   rec.slackURL(),
				DiscordWebhook: rec.discordURL(),
				Timeout:        5 * time.Second,
			}
			c.run(n)

			sent := rec.got()
			if len(sent) != 2 {
				t.Fatalf("posted %d times, want one per channel", len(sent))
			}
			// Both channels carry the same text; only the field differs.
			wantSlack := messageOf(t, captured[i*2].Body)
			wantDiscord := messageOf(t, captured[i*2+1].Body)
			if wantSlack != wantDiscord {
				t.Fatalf("the capture disagrees between channels: %q vs %q", wantSlack, wantDiscord)
			}
			for j, body := range sent {
				if got := messageOf(t, body); got != wantSlack {
					t.Errorf("channel %d sent\n  %q\nPython sent\n  %q", j, got, wantSlack)
				}
			}
		})
	}
}

// TestFieldNamesMatchTheServices: Slack reads `text`, Discord reads
// `content`. Swapping them produces a 200 and an empty message, which is the
// kind of failure nobody notices.
func TestFieldNamesMatchTheServices(t *testing.T) {
	rec := newRecorder(t, 0)
	n := &Notifier{SlackWebhook: rec.slackURL(), DiscordWebhook: rec.discordURL()}
	n.Test(context.Background(), "hello")

	sent := rec.got()
	if len(sent) != 2 {
		t.Fatalf("posted %d times, want 2", len(sent))
	}
	var slack, discord map[string]string
	if err := json.Unmarshal([]byte(sent[0]), &slack); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(sent[1]), &discord); err != nil {
		t.Fatal(err)
	}
	if _, ok := slack["text"]; !ok {
		t.Errorf("the slack payload has no `text`: %v", slack)
	}
	if _, ok := discord["content"]; !ok {
		t.Errorf("the discord payload has no `content`: %v", discord)
	}

	for _, ct := range rec.contentTypes() {
		if ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
	}
}

// ---- The reason line -----------------------------------------------------

func TestFirstLine(t *testing.T) {
	tests := []struct {
		name   string
		reason string
		want   string
	}{
		{name: "empty becomes unknown", reason: "", want: "unknown"},
		{name: "whitespace only becomes unknown", reason: "  \n\t ", want: "unknown"},
		{name: "a one-line reason survives", reason: "spec ambiguous", want: "spec ambiguous"},
		{name: "surrounding whitespace is trimmed", reason: "  spec ambiguous  ", want: "spec ambiguous"},
		{name: "only the first line is sent", reason: "first\nsecond\nthird", want: "first"},
		{name: "a carriage return also ends the line", reason: "first\r\nsecond", want: "first"},
		{
			name:   "past 200 characters it is cut",
			reason: strings.Repeat("x", 250),
			want:   strings.Repeat("x", 200),
		},
		{
			// Python slices a str, so the limit counts characters. Cutting
			// bytes would both shorten the message and split a rune.
			name:   "the limit counts characters, not bytes",
			reason: strings.Repeat("é", 250),
			want:   strings.Repeat("é", 200),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := firstLine(tt.reason); got != tt.want {
				t.Errorf("firstLine(%d chars) = %q (%d chars), want %d chars",
					len([]rune(tt.reason)), truncForMsg(got), len([]rune(got)), len([]rune(tt.want)))
			}
		})
	}
}

func truncForMsg(s string) string {
	r := []rune(s)
	if len(r) <= 40 {
		return s
	}
	return string(r[:40]) + "…"
}

// ---- Disabled is free ----------------------------------------------------

// TestDisabledNotifierDoesNothing. The engine builds one unconditionally and
// calls it on every block, so an unconfigured project must pay nothing and a
// nil one must not panic.
func TestDisabledNotifierDoesNothing(t *testing.T) {
	t.Run("the zero value", func(t *testing.T) {
		var n Notifier
		if n.Enabled() {
			t.Error("a notifier with no URLs is not enabled")
		}
		n.Blocked(context.Background(), "T-1", "boom")
		n.CIBlocked(context.Background(), "T-1", "", 1)
		if n.Test(context.Background(), "x") {
			t.Error("Test reported success with no channels")
		}
	})

	t.Run("a nil notifier", func(t *testing.T) {
		var n *Notifier
		if n.Enabled() {
			t.Error("nil is not enabled")
		}
		// Must not panic: the engine holds one of these whether or not the
		// project configured it.
		n.Blocked(context.Background(), "T-1", "boom")
		n.CIBlocked(context.Background(), "T-1", "", 1)
		if n.Test(context.Background(), "x") {
			t.Error("Test on nil reported success")
		}
	})
}

func TestEnabledWithOneChannel(t *testing.T) {
	rec := newRecorder(t, 0)

	t.Run("slack only", func(t *testing.T) {
		n := &Notifier{SlackWebhook: rec.slackURL()}
		if !n.Enabled() {
			t.Error("one URL is enough to be enabled")
		}
		before := len(rec.got())
		n.Test(context.Background(), "x")
		if got := len(rec.got()) - before; got != 1 {
			t.Errorf("posted %d times with one channel configured", got)
		}
	})

	t.Run("discord only", func(t *testing.T) {
		n := &Notifier{DiscordWebhook: rec.discordURL()}
		if !n.Enabled() {
			t.Error("one URL is enough to be enabled")
		}
	})
}

// ---- Failure is silent ---------------------------------------------------

// TestAFailingWebhookIsSwallowed is the property the whole package exists
// for: a broken webhook must never reach the dispatch loop.
func TestAFailingWebhookIsSwallowed(t *testing.T) {
	tests := []struct {
		name string
		url  func(rec *recorder) string
		code int
	}{
		{name: "a 500", url: func(r *recorder) string { return r.slackURL() }, code: 500},
		{name: "a 404", url: func(r *recorder) string { return r.slackURL() }, code: 404},
		{name: "a 403", url: func(r *recorder) string { return r.slackURL() }, code: 403},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := newRecorder(t, tt.code)
			n := &Notifier{SlackWebhook: tt.url(rec)}
			// Neither panics nor blocks; Blocked returns nothing at all.
			n.Blocked(context.Background(), "T-1", "boom")
			if n.Test(context.Background(), "x") {
				t.Errorf("Test reported success on a %d", tt.code)
			}
		})
	}

	t.Run("an unreachable host", func(t *testing.T) {
		// Port 1 on localhost: nothing listens, and the connection is
		// refused fast rather than hanging.
		n := &Notifier{SlackWebhook: "http://127.0.0.1:1/hook", Timeout: time.Second}
		n.Blocked(context.Background(), "T-1", "boom")
		if n.Test(context.Background(), "x") {
			t.Error("Test reported success against nothing")
		}
	})

	t.Run("a malformed URL", func(t *testing.T) {
		n := &Notifier{SlackWebhook: "http://[::1]:namedport/hook"}
		n.Blocked(context.Background(), "T-1", "boom")
		if n.Test(context.Background(), "x") {
			t.Error("Test reported success on a bad URL")
		}
	})
}

// TestOneChannelFailingDoesNotStopTheOther: an accept from either is an
// accept, which is what Python's `any_ok` means.
func TestOneChannelFailingDoesNotStopTheOther(t *testing.T) {
	good := newRecorder(t, 200)
	n := &Notifier{
		SlackWebhook:   "http://127.0.0.1:1/dead",
		DiscordWebhook: good.discordURL(),
		Timeout:        time.Second,
	}
	if !n.Test(context.Background(), "x") {
		t.Error("Test should report success when one channel accepted")
	}
	if got := len(good.got()); got != 1 {
		t.Errorf("the working channel received %d messages, want 1", got)
	}
}

// TestSlowWebhookTimesOut: a webhook that never answers must not hold the
// dispatch loop.
func TestSlowWebhookTimesOut(t *testing.T) {
	blocked := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-blocked // hang until the test lets go
	}))
	defer srv.Close()
	defer close(blocked)

	n := &Notifier{SlackWebhook: srv.URL + "/slack", Timeout: 150 * time.Millisecond}

	done := make(chan bool, 1)
	go func() { done <- n.Test(context.Background(), "x") }()

	select {
	case ok := <-done:
		if ok {
			t.Error("a hanging webhook reported success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the notifier did not give up on a hanging webhook")
	}
}

func TestNew(t *testing.T) {
	t.Run("a configured timeout is used", func(t *testing.T) {
		n := New("s", "d", 2.5)
		if n.Timeout != 2500*time.Millisecond {
			t.Errorf("Timeout = %v, want 2.5s", n.Timeout)
		}
	})
	t.Run("zero means the default", func(t *testing.T) {
		if got := New("s", "", 0).Timeout; got != DefaultTimeout {
			t.Errorf("Timeout = %v, want %v", got, DefaultTimeout)
		}
	})
}
