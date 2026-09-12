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

// frameReader turns a live SSE body into frames a test can take one at a time.
//
// One goroutine per stream, started when the stream opens — not one per read.
// Two readers on the same body is a data race, and it is the shape a "read
// some frames" helper falls into the moment a test wants to read, act, and
// read again.
type frameReader struct {
	events   chan eventPayload
	comments chan struct{}
	t        *testing.T
}

func newFrameReader(t *testing.T, body *bufio.Reader) *frameReader {
	t.Helper()
	fr := &frameReader{
		events:   make(chan eventPayload, 64),
		comments: make(chan struct{}, 64),
		t:        t,
	}
	go func() {
		defer close(fr.events)
		var pendingEvent string
		for {
			line, err := body.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\n")
			switch {
			case strings.HasPrefix(line, ": "):
				select {
				case fr.comments <- struct{}{}:
				default:
				}
			case strings.HasPrefix(line, "event: "):
				pendingEvent = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				if pendingEvent != "event" {
					return
				}
				var ev eventPayload
				if jsonErr := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); jsonErr != nil {
					return
				}
				fr.events <- ev
			}
		}
	}()
	return fr
}

// next waits for one event frame. ok is false when the deadline passed or the
// stream ended, which is what a test asserting "nothing arrives" checks.
func (fr *frameReader) next(timeout time.Duration) (ev eventPayload, ok bool) {
	fr.t.Helper()
	select {
	case ev, ok = <-fr.events:
		return ev, ok
	case <-time.After(timeout):
		return eventPayload{}, false
	}
}

func openStream(t *testing.T, srv *httptest.Server, path string) (*frameReader, func()) {
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
	return newFrameReader(t, bufio.NewReader(resp.Body)), func() {
		cancel()
		// The body is being read by the frameReader's goroutine, so a close
		// here races it and reports "use of closed connection" as often as
		// nil. Cancelling the request is what actually ends the stream; this
		// only releases the connection, and its error says nothing a test
		// could act on.
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

	ev, ok := body.next(3 * time.Second)
	if !ok {
		t.Fatal("nothing arrived")
	}
	if ev.TaskID != "T-2" || ev.EventType != "block" {
		t.Errorf("frame = %+v, want the event appended after connecting", ev)
	}
	// And it is the FORMATTED shape, the same one /api/events serves, so the
	// SPA's history and its live tail render identically.
	if ev.Severity != "block" || !strings.Contains(ev.Human, "waiting on T-1") {
		t.Errorf("frame is not formatted: %+v", ev)
	}
	// Nothing else follows: the two events that already existed must not
	// replay. A second frame here would be the whole log arriving late.
	if extra, more := body.next(1500 * time.Millisecond); more {
		t.Errorf("a second frame arrived (%+v); the pre-existing events replayed", extra)
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

	// Reading the first frame is the synchronisation, not a sleep: the frame
	// cannot arrive until a poll has delivered event 2 AND recorded its
	// position, which is exactly the state Python loses the next event in. A
	// sleep would be guessing at the same thing and would still be guessing
	// on a slow machine.
	first, ok := body.next(4 * time.Second)
	if !ok {
		t.Fatal("the stream never delivered the first event; nothing to lose the second one after")
	}

	f.appendEvent(state.Event{ID: 3, TaskID: "T-1", EventType: "block", TS: sameSecond})
	second, ok := body.next(4 * time.Second)
	if !ok {
		t.Fatal("the second event of the same second never arrived — this is the one Python drops")
	}
	if first.EventType != "dispatch" || second.EventType != "block" {
		t.Errorf("frames = %q, %q; want dispatch then block", first.EventType, second.EventType)
	}
	// Both really did carry the same timestamp, so the test is about the key
	// and not about two rows that happen to be a second apart.
	if first.TS != sameSecond || second.TS != sameSecond {
		t.Errorf("timestamps = %q, %q; the fixture is not exercising the collision",
			first.TS, second.TS)
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

	for i := 0; i < 2; i++ {
		ev, ok := body.next(4 * time.Second)
		if !ok {
			t.Fatalf("only %d frames arrived, want 2", i)
		}
		if ev.TaskID != "T-2" {
			t.Errorf("frame for %s reached a stream filtered to T-2", ev.TaskID)
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
	if _, ok := body.next(3 * time.Second); !ok {
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
		if ev, ok := body.next(2 * time.Second); ok {
			t.Errorf("got a frame (%+v) from a stream whose reads all fail", ev)
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
