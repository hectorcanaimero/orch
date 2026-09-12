// Package telemetry sends anonymous, opt-in usage pings — G8.6 (F4.9).
//
// Off by default. A project turns it on with `telemetry.enabled: true` in
// its own config.yaml; the standard DO_NOT_TRACK environment variable
// (https://consoledonottrack.com) always wins over that, on or off — see
// DoNotTrack. What one event carries, in full:
//
//   - the binary's own version
//   - the OS and architecture it's running on
//   - which subcommand was invoked (e.g. "status", "dashboard token rotate")
//   - how long that invocation took
//   - whether it succeeded
//   - a random per-installation id (~/.orch/install_id, see InstallID) that
//     ties events together without identifying a person or a project
//
// Never a file path, a prompt, a task id, or a project id/name — none of
// that reaches this package in the first place; Reporter has no field for
// any of it. See docs/TELEMETRY.md for this same list as shipped
// documentation, kept in step with the Event struct below.
package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"time"
)

// defaultEndpoint is intentionally empty.
//
// TODO(hectorcanaimero): point this at the real telemetry collection
// endpoint before this can send anything in a release build. Until it's
// set, Report is always a no-op regardless of Reporter.Enabled — there is
// nowhere to send an event, and this package will not guess one.
const defaultEndpoint = ""

// DefaultTimeout bounds how long a report may hold up the process exiting.
// Best-effort: a slow or unreachable collector must never make `orch`
// itself feel slow.
const DefaultTimeout = 2 * time.Second

// DoNotTrack reports whether the standard DO_NOT_TRACK environment
// variable opts this invocation out, regardless of what config.yaml says.
// Its mere presence is enough to opt out; "0" is the one value that does
// NOT, matching every other tool that honours this variable (see
// https://consoledonottrack.com).
func DoNotTrack() bool {
	v, set := os.LookupEnv("DO_NOT_TRACK")
	return set && v != "0"
}

// Event is exactly what one report sends — see the package doc comment
// for the "why only this" list. Every field here is safe to publish; if a
// field wouldn't be, it does not belong on this struct.
type Event struct {
	InstallID  string `json:"install_id"`
	Version    string `json:"version"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	Command    string `json:"command"`
	DurationMS int64  `json:"duration_ms"`
	Success    bool   `json:"success"`
}

// Reporter sends one Event per orch invocation, when enabled.
type Reporter struct {
	// Enabled is the already-resolved decision — the caller ANDs
	// config.yaml's telemetry.enabled with !DoNotTrack() before setting
	// this, so Report itself has exactly one policy question to ask
	// ("am I on"), which keeps it simple to test both ways.
	Enabled bool
	// Endpoint overrides defaultEndpoint. An empty value here still means
	// "no endpoint configured" and Report no-ops the same as when
	// disabled — see defaultEndpoint's own TODO for why that's the
	// current default everywhere.
	Endpoint  string
	InstallID string
	Version   string
	// Transport lets a caller (production code and tests alike) control
	// exactly how the HTTP request is sent, or capture it without a real
	// server. nil uses http.DefaultTransport.
	Transport http.RoundTripper
	// Timeout defaults to DefaultTimeout when zero or negative.
	Timeout time.Duration
	// Logger defaults to slog.Default(). Used only at Debug level — a
	// failed report is never surfaced to the operator as an error; the
	// command that triggered it already ran to completion by the time
	// Report is called.
	Logger *slog.Logger
}

// Report sends one event, best-effort. It never returns an error and is
// safe to call unconditionally on every invocation: a disabled Reporter,
// an unset endpoint, or a network failure all just mean nothing was sent.
func (r Reporter) Report(command string, duration time.Duration, success bool) {
	endpoint := r.Endpoint
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	if !r.Enabled || endpoint == "" {
		return
	}
	log := r.Logger
	if log == nil {
		log = slog.Default()
	}

	body, err := json.Marshal(Event{
		InstallID:  r.InstallID,
		Version:    r.Version,
		OS:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		Command:    command,
		DurationMS: duration.Milliseconds(),
		Success:    success,
	})
	if err != nil {
		log.Debug("telemetry: encode event", "err", err)
		return
	}

	timeout := r.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		log.Debug("telemetry: build request", "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	transport := r.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	client := &http.Client{Transport: transport, Timeout: timeout}

	resp, err := client.Do(req)
	if err != nil {
		log.Debug("telemetry: send event", "err", err)
		return
	}
	// The response body is never read — there is nothing in a 2xx/4xx/5xx
	// body this best-effort ping would act on — so closing is the only
	// obligation. A close error here changes nothing a caller could do
	// differently: the event either reached the collector or it didn't,
	// and this process is done with the connection either way.
	_ = resp.Body.Close()
}
