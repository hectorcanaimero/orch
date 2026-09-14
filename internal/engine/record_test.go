package engine

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/providers"
	"github.com/hectorcanaimero/orch/internal/state"
)

// openBackend gives each test its own project database. The engine writes
// through state.Backend and nothing else, so these tests exercise the real
// SQLite path — including the event-type validation, which is the whole
// reason the mapping in record.go has to be right.
func openBackend(t *testing.T) state.Backend {
	t.Helper()
	root := t.TempDir()
	db, _, err := state.Open(context.Background(), filepath.Join(root, "state", "orch.db"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close state: %v", err)
		}
	})
	b := state.NewSQLite(db, "testproj", root)
	if err := b.Bootstrap(context.Background(), []model.Task{{
		ID: "B-020", Phase: 1, Title: "a task", Model: "claude/haiku",
		Status: model.StatusTodo, EstimateHours: 1,
	}}); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if err := b.StartRun(context.Background(), "run-1", "auto"); err != nil {
		t.Fatalf("start run: %v", err)
	}
	return b
}

func recordDispatch(taskID string) Dispatch {
	return Dispatch{
		Req: providers.Request{
			TaskID: taskID,
			Route: model.RouteEntry{
				Backend:  model.BackendClaude,
				CLIModel: "claude-haiku-4-5-20251001",
			},
			SessionID: "3f2c9a10-7b4d-4e51-9c88-1d2e3f4a5b60",
		},
		PromptPath: "/tmp/prompts/B-020.txt",
		LogPath:    "/tmp/state/logs/B-020.log",
	}
}

func TestRecordStart(t *testing.T) {
	ctx := context.Background()
	b := openBackend(t)
	d := recordDispatch("B-020")
	sp := &Spawned{PID: 4242, StartedAt: time.Date(2026, 9, 11, 18, 30, 0, 0, time.UTC)}

	if err := RecordStart(ctx, b, "run-1", d, sp, 1); err != nil {
		t.Fatalf("RecordStart: %v", err)
	}

	events, err := b.Events(ctx, "B-020", 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	ev := events[0]
	if ev.EventType != EventDispatch {
		t.Errorf("EventType = %q, want %q", ev.EventType, EventDispatch)
	}
	if ev.Backend != string(model.BackendClaude) {
		t.Errorf("Backend = %q", ev.Backend)
	}
	if ev.TS != "2026-09-11T18:30:00Z" {
		t.Errorf("TS = %q", ev.TS)
	}
	// pid feeds the dedup hash, and the dashboard's in-flight view reads it.
	if got := ev.Extra["pid"]; !numericEquals(got, 4242) {
		t.Errorf("extra pid = %v (%T), want 4242", got, got)
	}
	if got := ev.Extra["cli_model"]; got != "claude-haiku-4-5-20251001" {
		t.Errorf("extra cli_model = %v", got)
	}
	if got := ev.Extra["attempt"]; !numericEquals(got, 1) {
		t.Errorf("extra attempt = %v", got)
	}
}

// TestRecordFinishSuccess walks the happy path all the way into SQLite: the
// spend row is written, the event is `success`, and the in-flight row is gone.
func TestRecordFinishSuccess(t *testing.T) {
	ctx := context.Background()
	b := openBackend(t)
	d := recordDispatch("B-020")
	start := time.Date(2026, 9, 11, 18, 30, 0, 0, time.UTC)
	sp := &Spawned{PID: 4242, StartedAt: start}

	if err := RecordStart(ctx, b, "run-1", d, sp, 1); err != nil {
		t.Fatalf("RecordStart: %v", err)
	}

	out := Outcome{
		Result: providers.Result{
			Success: true, ExitCode: 0,
			CostUSD: 0.0475971, TokensIn: 19, TokensOut: 603,
		},
		StartedAt: start,
		Duration:  90 * time.Second,
	}
	if err := RecordFinish(ctx, b, "run-1", d, out, 1); err != nil {
		t.Fatalf("RecordFinish: %v", err)
	}

	spend, err := b.SpendSince(ctx, string(model.BackendClaude), start.Add(-time.Hour))
	if err != nil {
		t.Fatalf("SpendSince: %v", err)
	}
	if len(spend) != 1 {
		t.Fatalf("got %d spend rows, want 1", len(spend))
	}
	s := spend[0]
	if s.CostUSD != 0.0475971 {
		t.Errorf("CostUSD = %v", s.CostUSD)
	}
	if s.TokensIn != 19 || s.TokensOut != 603 {
		t.Errorf("tokens = (%d,%d)", s.TokensIn, s.TokensOut)
	}
	if s.DurationS != 90 {
		t.Errorf("DurationS = %v, want 90", s.DurationS)
	}
	if s.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("Model = %q", s.Model)
	}

	events, err := b.Events(ctx, "B-020", 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want dispatch then success", len(events))
	}
	if events[1].EventType != EventSuccess {
		t.Errorf("EventType = %q, want %q", events[1].EventType, EventSuccess)
	}
	// Python's success event carries cost and duration, not an exit code.
	if _, ok := events[1].Extra["exit_code"]; ok {
		t.Error("a success event should not carry exit_code")
	}
	if got := events[1].Extra["cost_usd"]; !numericEquals(got, 0.0475971) {
		t.Errorf("extra cost_usd = %v", got)
	}
}

// #231: the denied tools reach the outcome event, success or not — it is the
// one place `orch events` and the dashboard can show why an agent that
// "succeeded" did nothing.
func TestRecordFinishCarriesPermissionDenials(t *testing.T) {
	ctx := context.Background()
	b := openBackend(t)
	d := recordDispatch("B-020")
	out := Outcome{
		Result: providers.Result{
			Success: true, PermissionDenials: []string{"Bash(git fetch)", "WebFetch"},
		},
		StartedAt: time.Date(2026, 9, 11, 18, 30, 0, 0, time.UTC),
	}
	if err := RecordFinish(ctx, b, "run-1", d, out, 1); err != nil {
		t.Fatalf("RecordFinish: %v", err)
	}
	events, err := b.Events(ctx, "B-020", 0)
	if err != nil || len(events) != 1 {
		t.Fatalf("Events = %v, %v", events, err)
	}
	got, _ := events[0].Extra["permission_denials"].([]any)
	if len(got) != 2 || got[0] != "Bash(git fetch)" || got[1] != "WebFetch" {
		t.Errorf("extra permission_denials = %#v", events[0].Extra["permission_denials"])
	}
}

// TestRecordFinishEventTypes is the mapping this package promises: Python's
// spellings, and no others. Every one is written through the real backend, so
// a type outside the closed set fails here rather than in production.
func TestRecordFinishEventTypes(t *testing.T) {
	tests := []struct {
		name    string
		out     Outcome
		want    string
		wantRsn string
	}{
		{
			name: "a clean exit is success",
			out:  Outcome{Result: providers.Result{Success: true}},
			want: EventSuccess,
		},
		{
			name: "an ordinary failure is fail",
			out: Outcome{
				Result:  providers.Result{ExitCode: 1, ErrorMessage: "429 rate limit"},
				Failure: providers.FailureRateLimit,
			},
			want:    EventFail,
			wantRsn: "429 rate limit",
		},
		{
			name: "a timeout is timeout, not fail",
			out: Outcome{
				Result:   providers.Result{ExitCode: -9, ErrorMessage: "orchestrator timeout after 300s"},
				Failure:  providers.FailureTimeout,
				TimedOut: true,
			},
			want:    EventTimeout,
			wantRsn: "orchestrator timeout after 300s",
		},
		{
			name: "a failure with no message still carries a reason",
			out: Outcome{
				Result:  providers.Result{ExitCode: 1},
				Failure: providers.FailureOther,
			},
			want:    EventFail,
			wantRsn: "unknown failure",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			b := openBackend(t)
			d := recordDispatch("B-020")

			if err := RecordFinish(ctx, b, "run-1", d, tt.out, 1); err != nil {
				t.Fatalf("RecordFinish: %v", err)
			}
			events, err := b.Events(ctx, "B-020", 0)
			if err != nil {
				t.Fatalf("Events: %v", err)
			}
			if len(events) != 1 {
				t.Fatalf("got %d events, want 1", len(events))
			}
			if events[0].EventType != tt.want {
				t.Errorf("EventType = %q, want %q", events[0].EventType, tt.want)
			}
			if tt.wantRsn != "" {
				if got := events[0].Extra["reason"]; got != tt.wantRsn {
					t.Errorf("extra reason = %v, want %q", got, tt.wantRsn)
				}
			}
		})
	}
}

// TestEventTypesAreAcceptedByTheBackend guards the closed set from the other
// side. state.AppendEvent rejects an unknown type (ErrUnknownEventType), so a
// constant here that drifts out of Python's vocabulary fails immediately
// rather than at the first real dispatch.
func TestEventTypesAreAcceptedByTheBackend(t *testing.T) {
	ctx := context.Background()
	b := openBackend(t)

	for _, et := range []string{EventDispatch, EventSuccess, EventFail, EventTimeout, EventIDSpoof} {
		t.Run(et, func(t *testing.T) {
			err := b.AppendEvent(ctx, "run-1", state.Event{
				RunID: "run-1", EventType: et, TaskID: "B-020",
				Backend: string(model.BackendClaude),
				TS:      "2026-09-11T18:30:0" + string(rune('0'+len(et)%10)) + "Z",
			})
			if err != nil {
				t.Errorf("AppendEvent(%q): %v", et, err)
			}
		})
	}
}

// TestEventTypesAreNotTheSupersededSpecNames pins the rename that #122 made.
// exit_ok and exit_err came from the superseded FR-STATE-7 list and no version
// of orch has ever emitted them; a port that reintroduced them would write
// rows the dashboard does not render.
func TestEventTypesAreNotTheSupersededSpecNames(t *testing.T) {
	if EventSuccess != "success" {
		t.Errorf("EventSuccess = %q, want %q", EventSuccess, "success")
	}
	if EventFail != "fail" {
		t.Errorf("EventFail = %q, want %q", EventFail, "fail")
	}
	for _, dead := range []string{"exit_ok", "exit_err", "resume_reset", "dry_run_planned"} {
		for _, live := range []string{EventDispatch, EventSuccess, EventFail, EventTimeout, EventIDSpoof} {
			if live == dead {
				t.Errorf("%q is a superseded spec name, not an orch event type", live)
			}
		}
	}
}

// TestRecordFinishRecordsSpendOnFailure: NFR-OBS-2 promises exactly one spend
// row per completed dispatch, failures included — a failed run has usually
// already been paid for, and missing it hides a real burn from the budget
// guardrail.
func TestRecordFinishRecordsSpendOnFailure(t *testing.T) {
	ctx := context.Background()
	b := openBackend(t)
	d := recordDispatch("B-020")
	start := time.Date(2026, 9, 11, 18, 30, 0, 0, time.UTC)

	out := Outcome{
		Result: providers.Result{
			ExitCode: 1, ErrorMessage: "429 rate limit",
			CostUSD: 0.0012, TokensIn: 50, TokensOut: 0,
		},
		Failure:   providers.FailureRateLimit,
		StartedAt: start,
		Duration:  3 * time.Second,
	}
	if err := RecordFinish(ctx, b, "run-1", d, out, 2); err != nil {
		t.Fatalf("RecordFinish: %v", err)
	}

	spend, err := b.SpendSince(ctx, string(model.BackendClaude), start.Add(-time.Hour))
	if err != nil {
		t.Fatalf("SpendSince: %v", err)
	}
	if len(spend) != 1 {
		t.Fatalf("got %d spend rows on a failed dispatch, want 1", len(spend))
	}
	if spend[0].CostUSD != 0.0012 {
		t.Errorf("CostUSD = %v, want the partial cost", spend[0].CostUSD)
	}

	events, err := b.Events(ctx, "B-020", 0)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if got := events[0].Extra["failure_class"]; got != string(providers.FailureRateLimit) {
		t.Errorf("extra failure_class = %v, want %q", got, providers.FailureRateLimit)
	}
	if got := events[0].Extra["attempt"]; !numericEquals(got, 2) {
		t.Errorf("extra attempt = %v, want 2", got)
	}
}

func TestTruncateReason(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want string
	}{
		{name: "empty becomes Python's placeholder", msg: "", want: "unknown failure"},
		{name: "a short message is untouched", msg: "boom", want: "boom"},
		{
			name: "exactly at the limit is untouched",
			msg:  strings.Repeat("x", reasonLimit),
			want: strings.Repeat("x", reasonLimit),
		},
		{
			name: "past the limit is cut",
			msg:  strings.Repeat("x", reasonLimit+50),
			want: strings.Repeat("x", reasonLimit),
		},
		{
			// Python slices a str by characters. Cutting bytes would both
			// shorten the message and split a rune in half.
			name: "multi-byte runes are counted as one each",
			msg:  strings.Repeat("á", reasonLimit+10),
			want: strings.Repeat("á", reasonLimit),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := truncateReason(tt.msg); got != tt.want {
				t.Errorf("truncateReason(len %d) = len %d, want len %d",
					len([]rune(tt.msg)), len([]rune(got)), len([]rune(tt.want)))
			}
		})
	}
}

func TestUTCSecond(t *testing.T) {
	// Second precision, always UTC, Z suffix — shared with scripts/task-*.sh,
	// so it is a contract rather than a preference.
	in := time.Date(2026, 9, 11, 18, 30, 45, 987654321, time.FixedZone("UTC-3", -3*3600))
	if got := utcSecond(in); got != "2026-09-11T21:30:45Z" {
		t.Errorf("utcSecond = %q, want %q", got, "2026-09-11T21:30:45Z")
	}
}

// numericEquals compares a value that made a JSON round trip through the
// event's extra column, where an int comes back as a float64.
func numericEquals(got any, want float64) bool {
	switch v := got.(type) {
	case float64:
		return v == want
	case int:
		return float64(v) == want
	case int64:
		return float64(v) == want
	default:
		return false
	}
}
