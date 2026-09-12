package telemetry

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

// failingTransport fails the test the moment RoundTrip is called — the
// test G8.6 exists to pass: with telemetry off (or misconfigured), no
// request ever goes out, not "goes out but the server ignores it."
type failingTransport struct{ t *testing.T }

func (f failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	f.t.Helper()
	f.t.Fatal("Report sent a request; want none")
	return nil, nil
}

func TestReportDoesNothingWhenDisabled(t *testing.T) {
	r := Reporter{
		Enabled:   false,
		Endpoint:  "http://example.invalid/collect",
		InstallID: "abc",
		Version:   "v1.2.3",
		Transport: failingTransport{t},
	}
	r.Report("status", 10*time.Millisecond, true)
}

func TestReportDoesNothingWithoutAnEndpoint(t *testing.T) {
	r := Reporter{
		Enabled:   true,
		Endpoint:  "", // falls back to defaultEndpoint, which is also ""
		InstallID: "abc",
		Version:   "v1.2.3",
		Transport: failingTransport{t},
	}
	r.Report("status", 10*time.Millisecond, true)
}

// capturingTransport records every request it sees instead of making one.
type capturingTransport struct {
	requests []*http.Request
	bodies   [][]byte
}

func (c *capturingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c.requests = append(c.requests, req)
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
	}
	c.bodies = append(c.bodies, body)
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(nil),
		Header:     make(http.Header),
	}, nil
}

func TestReportSendsExactlyOneEventWithTheDocumentedFields(t *testing.T) {
	ct := &capturingTransport{}
	r := Reporter{
		Enabled:   true,
		Endpoint:  "http://example.invalid/collect",
		InstallID: "install-123",
		Version:   "v9.9.9",
		Transport: ct,
	}
	r.Report("dashboard token rotate", 250*time.Millisecond, false)

	if len(ct.requests) != 1 {
		t.Fatalf("sent %d request(s), want exactly 1", len(ct.requests))
	}
	req := ct.requests[0]
	if req.Method != http.MethodPost {
		t.Errorf("method = %s, want POST", req.Method)
	}
	if req.URL.String() != "http://example.invalid/collect" {
		t.Errorf("URL = %s, want the configured endpoint", req.URL)
	}
	if ct := req.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var got Event
	if err := json.Unmarshal(ct.bodies[0], &got); err != nil {
		t.Fatalf("decode body %s: %v", ct.bodies[0], err)
	}
	want := Event{
		InstallID:  "install-123",
		Version:    "v9.9.9",
		Command:    "dashboard token rotate",
		DurationMS: 250,
		Success:    false,
	}
	if got.InstallID != want.InstallID || got.Version != want.Version ||
		got.Command != want.Command || got.DurationMS != want.DurationMS ||
		got.Success != want.Success {
		t.Errorf("event = %+v, want %+v (OS/Arch excluded, host-dependent)", got, want)
	}
	if got.OS == "" || got.Arch == "" {
		t.Errorf("OS/Arch not populated: %+v", got)
	}
}

func TestDoNotTrack(t *testing.T) {
	cases := []struct {
		name string
		set  bool
		val  string
		want bool
	}{
		{"unset", false, "", false},
		{"empty string", true, "", true},
		{"1", true, "1", true},
		{"true", true, "true", true},
		{"0 does not opt out", true, "0", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv("DO_NOT_TRACK", tc.val)
			} else {
				// t.Setenv has no "unset" form, so this one restores the
				// prior value itself rather than relying on it.
				prior, wasSet := os.LookupEnv("DO_NOT_TRACK")
				if err := os.Unsetenv("DO_NOT_TRACK"); err != nil {
					t.Fatalf("unsetenv: %v", err)
				}
				t.Cleanup(func() {
					if wasSet {
						_ = os.Setenv("DO_NOT_TRACK", prior)
					}
				})
			}
			if got := DoNotTrack(); got != tc.want {
				t.Errorf("DoNotTrack() = %v, want %v", got, tc.want)
			}
		})
	}
}
