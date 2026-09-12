package dashboard

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/state"
)

// streamServer is a real listener, because that is what a stream needs: an
// httptest.ResponseRecorder has no flush and no connection to close, so a test
// against one would only be testing the loop's arithmetic.
func streamServer(t *testing.T, f *fakeState) *httptest.Server {
	t.Helper()
	root := writeProject(t, "spec_root: specs\n", testTasksJSON)
	s, err := New(cfg(ProfileOperator, ""), Options{
		Static: spaHandler(t),
		State:  f,
		Paths:  config.Paths{Root: root, ID: "demo", ConfigYAML: filepath.Join(root, "config.yaml")},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

// readFrames reads SSE frames until it has n of them or the deadline passes.
// Comment frames (`: keepalive`) are counted separately — they are not events
// and a test that confused the two would pass on a silent stream.
func readFrames(t *testing.T, body *bufio.Reader, n int, deadline time.Duration) (events []eventPayload, comments int) {
	t.Helper()
	done := time.After(deadline)
	lines := make(chan string, 64)
	go func() {
		for {
			line, err := body.ReadString('\n')
			if err != nil {
				close(lines)
				return
			}
			lines <- line
		}
	}()

	var pendingEvent string
	for len(events) < n {
		select {
		case <-done:
			return events, comments
		case line, ok := <-lines:
			if !ok {
				return events, comments
			}
			line = strings.TrimRight(line, "\n")
			switch {
			case strings.HasPrefix(line, ": "):
				comments++
			case strings.HasPrefix(line, "event: "):
				pendingEvent = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				if pendingEvent != "event" {
					t.Errorf("frame arrived with event name %q, want %q", pendingEvent, "event")
				}
				var ev eventPayload
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
					t.Fatalf("frame is not JSON: %v", err)
				}
				events = append(events, ev)
			}
		}
	}
	return events, comments
}

func openStream(t *testing.T, srv *httptest.Server, path string) (*bufio.Reader, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+path, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	// Nginx buffers proxied responses by default, which for a stream means the
	// client sees nothing until the buffer fills.
	if resp.Header.Get("X-Accel-Buffering") != "no" {
		t.Error("X-Accel-Buffering is not set; a proxy would buffer this stream")
	}
	return bufio.NewReader(resp.Body), func() {
		cancel()
		_ = resp.Body.Close()
	}
}

// The tail carries what happens AFTER the client connected — not the whole log
// replayed as new. Python starts from an empty timestamp and replays
// everything; the SPA then deduplicates it against the history it already
// fetched, and the metrics page invalidates its task query once per replayed
// row.
func TestEventStreamStartsFromNowNotFromTheBeginning(t *testing.T) {
	f := &fakeState{events: []state.Event{
		{ID: 1, TaskID: "T-1", EventType: "dispatch", TS: "2026-09-01T10:00:00Z"},
		{ID: 2, TaskID: "T-1", EventType: "success", TS: "2026-09-01T10:00:05Z"},
	}}
	srv := streamServer(t, f)
	body, closeStream := openStream(t, srv, "/api/events/stream")
	defer closeStream()

	f.appendEvent(state.Event{ID: 3, TaskID: "T-2", EventType: "block",
		TS: "2026-09-01T10:01:00Z", Extra: map[string]any{"reason": "waiting on T-1"}})

	events, _ := readFrames(t, body, 1, 3*time.Second)
	if len(events) != 1 {
		t.Fatalf("got %d frames, want exactly 1 — the two pre-existing events must not replay", len(events))
	}
	if events[0].TaskID != "T-2" || events[0].EventType != "block" {
		t.Errorf("frame = %+v, want the event appended after connecting", events[0])
	}
	// And it is the FORMATTED shape, the same one /api/events serves, so the
	// SPA's history and its live tail render identically.
	if events[0].Severity != "block" || !strings.Contains(events[0].Human, "waiting on T-1") {
		t.Errorf("frame is not formatted: %+v", events[0])
	}
}

// Two events written in the same SECOND both arrive. This is bug 25: Python
// polls `ts > last_ts` against second-resolution timestamps, so an event
// written in the same second as the last one delivered — but after the poll
// that delivered it — is dropped and never seen again. Keying on the row id
// closes that window.
func TestEventStreamDeliversTwoEventsInTheSameSecond(t *testing.T) {
	f := &fakeState{events: []state.Event{{ID: 1, TaskID: "T-1", EventType: "dispatch",
		TS: "2026-09-01T09:59:00Z"}}}
	srv := streamServer(t, f)
	body, closeStream := openStream(t, srv, "/api/events/stream")
	defer closeStream()

	const sameSecond = "2026-09-01T10:00:00Z"
	f.appendEvent(state.Event{ID: 2, TaskID: "T-1", EventType: "dispatch", TS: sameSecond})
	// Long enough for the first poll to have delivered event 2 and recorded
	// its position — which is exactly the state Python loses the next one in.
	time.Sleep(900 * time.Millisecond)
	f.appendEvent(state.Event{ID: 3, TaskID: "T-1", EventType: "block", TS: sameSecond})

	events, _ := readFrames(t, body, 2, 4*time.Second)
	if len(events) != 2 {
		t.Fatalf("got %d frames, want 2 — the second event of the same second is the one Python drops", len(events))
	}
	if events[0].EventType != "dispatch" || events[1].EventType != "block" {
		t.Errorf("frames = %q, %q; want dispatch then block", events[0].EventType, events[1].EventType)
	}
}

// `?task_id=` filters server-side, so another task's payload never reaches the
// wire. The position still advances past the rows it skipped, or a busy
// neighbour would make the tail re-read the same rows forever.
func TestEventStreamFiltersByTask(t *testing.T) {
	f := &fakeState{}
	srv := streamServer(t, f)
	body, closeStream := openStream(t, srv, "/api/events/stream?task_id=T-2")
	defer closeStream()

	f.appendEvent(state.Event{ID: 1, TaskID: "T-1", EventType: "dispatch", TS: "2026-09-01T10:00:00Z"})
	f.appendEvent(state.Event{ID: 2, TaskID: "T-2", EventType: "dispatch", TS: "2026-09-01T10:00:01Z"})
	f.appendEvent(state.Event{ID: 3, TaskID: "T-1", EventType: "success", TS: "2026-09-01T10:00:02Z"})
	f.appendEvent(state.Event{ID: 4, TaskID: "T-2", EventType: "success", TS: "2026-09-01T10:00:03Z"})

	events, _ := readFrames(t, body, 2, 4*time.Second)
	if len(events) != 2 {
		t.Fatalf("got %d frames, want 2", len(events))
	}
	for _, e := range events {
		if e.TaskID != "T-2" {
			t.Errorf("frame for %s reached a stream filtered to T-2", e.TaskID)
		}
	}
}

// A client that goes away ends the stream rather than leaving a goroutine
// polling SQLite for the life of the process.
func TestEventStreamStopsWhenTheClientLeaves(t *testing.T) {
	f := &fakeState{}
	srv := streamServer(t, f)
	body, closeStream := openStream(t, srv, "/api/events/stream")

	f.appendEvent(state.Event{ID: 1, TaskID: "T-1", EventType: "dispatch", TS: "2026-09-01T10:00:00Z"})
	if events, _ := readFrames(t, body, 1, 3*time.Second); len(events) != 1 {
		t.Fatalf("the stream never delivered anything")
	}

	closeStream()
	// The handler notices through the request context. Nothing here can
	// observe the goroutine directly, so what this asserts is that the
	// server shuts down without hanging — httptest.Server.Close blocks on
	// outstanding handlers, and the t.Cleanup would time the test out.
	srv.Close()
}

// A read that fails before the first byte is a normal 500. After the first
// byte there is no status left to change, so the stream just ends and the
// client's EventSource reconnects.
func TestEventStreamReadFailures(t *testing.T) {
	boom := errors.New("database is locked")

	t.Run("before the first byte", func(t *testing.T) {
		srv := streamServer(t, &fakeState{latestErr: boom})
		resp, err := srv.Client().Get(srv.URL + "/api/events/stream")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500", resp.StatusCode)
		}
	})

	t.Run("mid-stream", func(t *testing.T) {
		f := &fakeState{sinceErr: boom}
		srv := streamServer(t, f)
		// The headers still say 200 — they were written before the poll that
		// failed, which is the whole reason this cannot be a 500.
		body, closeStream := openStream(t, srv, "/api/events/stream")
		defer closeStream()
		if events, _ := readFrames(t, body, 1, 2*time.Second); len(events) != 0 {
			t.Errorf("got %d frames from a stream whose reads all fail", len(events))
		}
	})
}

// The stream is gated like every other data route, and is not on the
// stakeholder allow-list: a log tail is operator data.
func TestEventStreamIsGated(t *testing.T) {
	root := writeProject(t, "spec_root: specs\n", testTasksJSON)
	s, err := New(cfg(ProfileStakeholder, "test-token-stakeholder"), Options{
		Static: spaHandler(t),
		State:  &fakeState{},
		Paths:  config.Paths{Root: root, ID: "demo", ConfigYAML: filepath.Join(root, "config.yaml")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp := get(t, s, "/api/events/stream"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no token: status = %d, want 401", resp.StatusCode)
	}
	if resp := get(t, s, "/api/events/stream?token=test-token-stakeholder"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("with a token: status = %d, want 403", resp.StatusCode)
	}
}

// One data line per frame, and the JSON never contains a newline that would
// end the frame early.
func TestWriteSSEFrameShape(t *testing.T) {
	rec := httptest.NewRecorder()
	err := writeSSE(rec, "event", eventPayload{
		TaskID: "T-1", EventType: "block", Human: "line one\nline two", Severity: "block",
	})
	if err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	if !strings.HasPrefix(body, "event: event\ndata: ") || !strings.HasSuffix(body, "\n\n") {
		t.Errorf("frame = %q", body)
	}
	// The newline inside the payload survives as an escape, not as a real
	// newline — one `data:` line, always.
	if strings.Count(body, "\n") != 3 {
		t.Errorf("frame has %d newlines, want 3 (event, data, blank): %q",
			strings.Count(body, "\n"), body)
	}
	if !strings.Contains(body, `line one\nline two`) {
		t.Errorf("the payload's newline was not escaped: %q", body)
	}
}
