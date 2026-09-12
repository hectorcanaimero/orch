package cli_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hectorcanaimero/orch/internal/cli"
)

// TestNotifyExitCodes ports `_run_notify_subcommand`'s exit codes.
//
// The digest cases run against the committed parity fixture, which has no
// webhook configured — which is the point for `--send`: the digest must still
// reach stdout before the failure, because an operator who cron'd
// `orch notify digest --send` and lost the webhook still wants the text in
// the mail the cron daemon sends them.
func TestNotifyExitCodes(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"digest prints", []string{"notify", "digest"}, 0},
		{"digest in english", []string{"notify", "digest", "--language", "en"}, 0},
		{"digest with a language nobody has", []string{"notify", "digest", "--language", "fr"}, 2},
		{"test with no webhook", []string{"notify", "test"}, 1},
		{"send with no webhook", []string{"notify", "digest", "--send"}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, common := newTestProject(t)
			if got := cli.Run("v-test", append(tc.args, common...)); got != tc.want {
				t.Errorf("exit = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestNotifySendPostsWhatItPrinted is the property the two halves of `digest`
// have to share: the bytes on stdout and the bytes in the webhook body are
// the same digest, not two renders of it.
//
// A local httptest server stands in for Slack. The payload shape is Slack's —
// `{"text": ...}` — which internal/notify already pins against real captured
// POSTs; what this adds is that the CLI hands it the text it showed the
// operator.
func TestNotifySendPostsWhatItPrinted(t *testing.T) {
	var (
		mu     sync.Mutex
		bodies []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		bodies = append(bodies, slackText(t, r.Body))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	root, common := newTestProject(t)
	writeWebhookConfig(t, root, srv.URL)

	printed := captureStdout(t, func() {
		if got := cli.Run("v-test", append([]string{"notify", "digest", "--send"}, common...)); got != 0 {
			t.Errorf("exit = %d, want 0", got)
		}
	})

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 1 {
		t.Fatalf("the webhook received %d POSTs, want exactly 1", len(bodies))
	}
	if bodies[0] != printed {
		t.Errorf("the webhook got different bytes than stdout.\n--- posted ---\n%s\n--- printed ---\n%s",
			bodies[0], printed)
	}
	if !strings.Contains(printed, "Proyecto") {
		t.Errorf("digest does not look like the Spanish summary:\n%s", printed)
	}
}

// TestNotifyDigestWithoutSendPostsNothing is rule 25: "exit 0 and the right
// text" cannot tell a command that skipped the POST from one that made it and
// ignored the answer. The server fails the test if it is touched at all.
func TestNotifyDigestWithoutSendPostsNothing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the webhook was called without --send: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	root, common := newTestProject(t)
	writeWebhookConfig(t, root, srv.URL)

	if got := cli.Run("v-test", append([]string{"notify", "digest"}, common...)); got != 0 {
		t.Errorf("exit = %d, want 0", got)
	}
}

// TestNotifyDigestLanguageOverridesTheConfig proves `--language` beats
// `dashboard.summary_language` rather than merely working when they agree —
// the config here says `es` and the flag says `en`.
func TestNotifyDigestLanguageOverridesTheConfig(t *testing.T) {
	root, common := newTestProject(t)
	writeConfig(t, root, "dashboard:\n  summary_language: es\n")

	spanish := captureStdout(t, func() {
		_ = cli.Run("v-test", append([]string{"notify", "digest"}, common...))
	})
	english := captureStdout(t, func() {
		_ = cli.Run("v-test", append([]string{"notify", "digest", "--language", "en"}, common...))
	})

	if !strings.Contains(spanish, "Proyecto") {
		t.Errorf("config language ignored; got:\n%s", spanish)
	}
	if !strings.Contains(english, "Project") || strings.Contains(english, "Proyecto") {
		t.Errorf("--language en did not override the config; got:\n%s", english)
	}
}

func writeWebhookConfig(t *testing.T, root, url string) {
	t.Helper()
	writeConfig(t, root, "notifications:\n  slack_webhook: "+url+"\n  timeout_s: 5\n")
}

func writeConfig(t *testing.T, root, body string) {
	t.Helper()
	path := filepath.Join(root, ".orchestrator", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("create .orchestrator: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}
}

// captureStdout swaps os.Stdout for a pipe around fn.
//
// cli.Run writes through cobra's default writer, which is os.Stdout — the
// same path a real invocation takes. Reading it back is the only way to
// assert on what an operator actually sees, and this test is about exactly
// that.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	saved := os.Stdout
	os.Stdout = w

	// The read runs in a goroutine because the pipe's buffer is finite: a
	// digest longer than it would block the command under test. The error
	// comes back over the channel rather than being reported from the
	// goroutine, so the failure is attributed on the test's own goroutine.
	type capture struct {
		text string
		err  error
	}
	done := make(chan capture, 1)
	go func() {
		raw, err := io.ReadAll(r)
		done <- capture{text: string(raw), err: err}
	}()

	fn()

	os.Stdout = saved
	if err := w.Close(); err != nil {
		t.Fatalf("close the pipe: %v", err)
	}
	got := <-done
	if got.err != nil {
		t.Fatalf("reading the captured stdout: %v", got.err)
	}
	out := got.text
	if err := r.Close(); err != nil {
		t.Fatalf("close the read end: %v", err)
	}
	return out
}

// TestNotifyTestPostsTheMessageAndExitsZero is the success path: a webhook
// that accepts. The exit code is the machine-readable half of this command,
// so "1 when it fails" is only half a contract — an operator wiring a webhook
// needs 0 when it works, and needs the message that lands in the channel to
// be the one they can recognise.
func TestNotifyTestPostsTheMessageAndExitsZero(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			// Python's default, word for word. It has to be tellable from a
			// real alert by whoever is looking at the channel.
			name: "the default message",
			args: []string{"notify", "test"},
			want: "orch: notifier test — if you see this, the webhook works.",
		},
		{
			name: "a custom message",
			args: []string{"notify", "test", "--message", "ping from the deploy box"},
			want: "ping from the deploy box",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var (
				mu     sync.Mutex
				bodies []string
			)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				bodies = append(bodies, slackText(t, r.Body))
				mu.Unlock()
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			root, common := newTestProject(t)
			writeWebhookConfig(t, root, srv.URL)

			if got := cli.Run("v-test", append(tc.args, common...)); got != 0 {
				t.Fatalf("exit = %d, want 0", got)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(bodies) != 1 {
				t.Fatalf("the webhook received %d POSTs, want exactly 1", len(bodies))
			}
			if bodies[0] != tc.want {
				t.Errorf("posted %q, want %q", bodies[0], tc.want)
			}
		})
	}
}

// A channel that rejects is exit 1, not exit 0 with a shrug. The whole point
// of the command is finding out before a run depends on it, so a 500 from
// Slack has to be as loud as no webhook at all.
func TestNotifyTestFailsWhenTheChannelRejects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	root, common := newTestProject(t)
	writeWebhookConfig(t, root, srv.URL)

	if got := cli.Run("v-test", append([]string{"notify", "test"}, common...)); got != 1 {
		t.Errorf("exit = %d, want 1 — a rejected message is a failed test", got)
	}
}

// slackText reads a Slack webhook body and pulls the message out of it.
//
// Both steps fail the test rather than returning a zero value, and for the
// same reason: an unread or undecodable body surfaces as an empty string, and
// the comparison then fails with `want <the digest>, got ""` — which sends
// the reader to look at the digest when the problem is the payload.
func slackText(t *testing.T, body io.Reader) string {
	t.Helper()
	raw, err := io.ReadAll(body)
	if err != nil {
		t.Errorf("reading the webhook body: %v", err)
		return ""
	}
	var payload struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Errorf("the webhook body is not Slack's {\"text\": ...} shape: %v\n%s", err, raw)
		return ""
	}
	return payload.Text
}
