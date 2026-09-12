package dashboard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// `/api/events/stream` — the live tail, ported from `_stream` + `tail_events`
// in `orchestrator/dashboard/log_stream.py`.
//
// The only endpoint in this package whose response never ends, which makes
// three things its own problem rather than the server's.

// streamPoll is how often the tail looks for new rows. Python's 0.5s: fast
// enough that a log line feels live, slow enough that an idle dashboard is not
// a busy loop against SQLite.
const streamPoll = 500 * time.Millisecond

// streamHeartbeat is how often a comment frame goes out on an idle stream.
//
// Not decoration. The server sets a 30s write timeout for every other route,
// and a response that stays open for hours must clear it — see the
// ResponseController call below. With the deadline gone, a silent connection
// has nothing proving it is alive, and the proxies a tunnel puts in the path
// close idle connections. An SSE comment is ignored by every EventSource
// client and costs two bytes a minute.
const streamHeartbeat = 25 * time.Second

// streamBatch caps how many rows one poll may deliver. A tail that fell behind
// catches up over several polls rather than writing ten thousand frames into a
// socket the client is not reading.
const streamBatch = 500

func (s *Server) streamRoutes() []route {
	return []route{
		{pattern: "GET /api/events/stream", name: "api_events_stream", handler: s.handleEventStream},
	}
}

func (s *Server) handleEventStream(w http.ResponseWriter, r *http.Request) {
	if s.state == nil {
		s.failRead(w, "event stream", errNoBackend)
		return
	}
	ctx := r.Context()

	// Where the tail starts, and the first divergence from Python: the NEWEST
	// id, so the stream carries only what happens after this request. Python
	// starts from an empty timestamp, which replays the whole log as "new" —
	// the SPA deduplicates it against the history it already fetched, and the
	// metrics page invalidates its task query once per replayed row.
	lastID, err := s.state.LatestEventID(ctx)
	if err != nil {
		s.failRead(w, "event stream", err)
		return
	}

	// The write deadline has to go before the first byte. Every other route
	// answers in milliseconds and wants the 30s timeout; this one is open
	// until the client leaves, and would otherwise be cut off mid-stream at
	// 30 seconds with no error the client could tell from a network fault.
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		// Not fatal: a ResponseWriter that cannot clear its deadline still
		// streams, just with the server's timeout in force. Logged rather
		// than refused, because 30 seconds of live tail beats none.
		s.log.Warn("event stream: could not clear the write deadline", "err", err)
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	// Nginx buffers proxied responses by default, which for a stream means
	// the client sees nothing until the buffer fills — a tunnel through one
	// would look like a dead connection. Python sets the same header.
	h.Set("X-Accel-Buffering", "no")
	h.Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	if err := rc.Flush(); err != nil {
		return
	}

	taskID := r.URL.Query().Get("task_id")
	ticker := time.NewTicker(streamPoll)
	defer ticker.Stop()
	lastWrite := time.Now()

	for {
		select {
		case <-ctx.Done():
			// The client went away. Not an error: it is how every one of
			// these ends.
			return
		case <-ticker.C:
		}

		events, err := s.state.EventsSince(ctx, lastID, streamBatch)
		if err != nil {
			// A read that fails mid-stream ends the stream. The status line
			// is long gone, so there is no code to change — the client's
			// EventSource reconnects, which is the right outcome for a
			// database that was busy for a moment.
			s.log.Error("event stream: read failed", "err", err)
			return
		}

		wrote := false
		for _, e := range events {
			lastID = e.ID
			if taskID != "" && e.TaskID != taskID {
				// Filtered server-side, so the payload never hits the wire —
				// but the id still advances, or the tail would re-read the
				// same rows forever on a project where another task is busy.
				continue
			}
			if err := writeSSE(w, "event", formatEvent(e)); err != nil {
				return
			}
			wrote = true
		}

		if !wrote && time.Since(lastWrite) >= streamHeartbeat {
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			wrote = true
		}
		if wrote {
			if err := rc.Flush(); err != nil {
				return
			}
			lastWrite = time.Now()
		}
	}
}

// writeSSE writes one frame: `event: <name>`, then one `data:` line, then the
// blank line that ends it.
//
// The payload is encoded into a buffer first. A frame half-written because the
// value would not encode is a frame the client parses as garbage, and there is
// no taking bytes back off a stream.
func writeSSE(w http.ResponseWriter, name string, payload any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(payload); err != nil {
		return fmt.Errorf("encoding an SSE payload: %w", err)
	}
	// Encode leaves a trailing newline, and a newline inside `data:` would end
	// the frame early.
	body := bytes.TrimRight(buf.Bytes(), "\n")

	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, body); err != nil {
		return err
	}
	return nil
}
