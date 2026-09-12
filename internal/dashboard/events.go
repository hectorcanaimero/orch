package dashboard

import (
	"fmt"
	"strconv"

	"github.com/hectorcanaimero/orch/internal/state"
)

// Event presentation, ported from `format_event` in
// `orchestrator/dashboard/log_stream.py`.
//
// The server formats rather than the SPA because the severity buckets and the
// human line are the log view's contract with every client, SSE included: the
// live stream and the one-shot history have to produce identical rows or the
// page visibly changes what it already drew when a refresh replaces one with
// the other.

// severityByEvent drives the colour. Anything not listed is "muted" — grey,
// visible, not shouting. The default matters more than the entries: a new
// event type added by a newer orch still renders.
var severityByEvent = map[string]string{
	"success":           "ok",
	"fail":              "err",
	"timeout":           "err",
	"retry":             "warn",
	"block":             "block",
	"dispatch":          "info",
	"escalate":          "warn",
	"resume_adopt":      "info",
	"resume_revert":     "warn",
	"id_spoof_detected": "err",
	"flock_contention":  "err",
	"reconciled":        "info",
}

// iconByEvent is the short marker at the head of the human line. ASCII on
// purpose: this string goes to a terminal as often as to a browser.
var iconByEvent = map[string]string{
	"success":           "OK",
	"fail":              "FAIL",
	"timeout":           "TIMEOUT",
	"retry":             "RETRY",
	"block":             "BLOCK",
	"dispatch":          "->",
	"escalate":          "ESC",
	"resume_adopt":      "ADOPT",
	"resume_revert":     "REVERT",
	"id_spoof_detected": "SPOOF",
	"flock_contention":  "LOCK",
	"reconciled":        "RECON",
}

// eventPayload is one formatted event.
type eventPayload struct {
	TS        string         `json:"ts"`
	TaskID    string         `json:"task_id"`
	EventType string         `json:"event_type"`
	Backend   string         `json:"backend"`
	Human     string         `json:"human"`
	Severity  string         `json:"severity"`
	Raw       map[string]any `json:"raw"`
}

// formatEvent never fails. A row with an unknown type, a missing timestamp
// and no extra still produces something a human can read — the alternative is
// a log view that goes blank exactly when something has gone wrong.
func formatEvent(e state.Event) eventPayload {
	etype := e.EventType
	if etype == "" {
		etype = "?"
	}
	taskID := e.TaskID
	if taskID == "" {
		taskID = "?"
	}

	// The clock part of an ISO timestamp, positionally. Python slices
	// `ts[11:19]` and falls back to the whole string when it is too short;
	// a malformed timestamp then shows as itself rather than as a panic.
	hhmmss := e.TS
	if len(e.TS) >= 19 {
		hhmmss = e.TS[11:19]
	}

	severity, ok := severityByEvent[etype]
	if !ok {
		severity = "muted"
	}
	icon, ok := iconByEvent[etype]
	if !ok {
		icon = "*"
	}

	return eventPayload{
		TS:        e.TS,
		TaskID:    taskID,
		EventType: etype,
		Backend:   e.Backend,
		Human:     fmt.Sprintf("[%s] %s %s %s%s", hhmmss, icon, taskID, etype, eventTail(etype, e.Extra)),
		Severity:  severity,
		Raw:       rawEvent(e),
	}
}

// eventTail is the short explanation after the event name:
//
//	dispatch → " -> claude-sonnet-4-6"
//	success  → " (12m 34s, $0.210)"
//	fail     → " (retry 2/3)"
//	block    → " (dep X-42 not done)"
func eventTail(etype string, extra map[string]any) string {
	switch etype {
	case "dispatch":
		tail := ""
		if m := extraString(extra, "cli_model"); m != "" {
			tail = " -> " + m
		}
		if n, ok := extraInt(extra, "attempt"); ok && n > 1 {
			tail += fmt.Sprintf(" (attempt %s)", extraRaw(extra, "attempt"))
		}
		return tail
	case "success":
		dur := formatDuration(extra["duration_s"])
		cost := ""
		if c, ok := extraFloat(extra, "cost_usd"); ok {
			cost = fmt.Sprintf(", $%.3f", c)
		}
		if dur == "" && cost == "" {
			return ""
		}
		return " (" + dur + cost + ")"
	case "fail", "timeout":
		attempt := extraRaw(extra, "attempt")
		maxAttempts := extraRaw(extra, "max_attempts")
		switch {
		case attempt != "" && maxAttempts != "":
			return fmt.Sprintf(" (retry %s/%s)", attempt, maxAttempts)
		case attempt != "":
			return fmt.Sprintf(" (attempt %s)", attempt)
		}
		return ""
	case "block":
		reason := extraString(extra, "reason")
		if reason == "" {
			reason = extraString(extra, "blocked_dep")
		}
		if reason == "" {
			return ""
		}
		return " (" + reason + ")"
	case "retry":
		if attempt := extraRaw(extra, "attempt"); attempt != "" {
			return fmt.Sprintf(" (attempt %s)", attempt)
		}
		return ""
	}
	return ""
}

// formatDuration is `612.4` → "10m 12s", `45.2` → "45s", missing → "".
func formatDuration(raw any) string {
	f, ok := toFloat(raw)
	if !ok {
		return ""
	}
	total := int(f)
	if total < 60 {
		return fmt.Sprintf("%ds", total)
	}
	m, s := total/60, total%60
	if m < 60 {
		return fmt.Sprintf("%dm %02ds", m, s)
	}
	h := m / 60
	m %= 60
	return fmt.Sprintf("%dh %02dm", h, m)
}

// rawEvent is the untouched row the SPA keeps for its detail panel. Built
// rather than passed through because `state.Event` is a struct and Python's
// `raw` is the dict it read.
func rawEvent(e state.Event) map[string]any {
	extra := e.Extra
	if extra == nil {
		extra = map[string]any{}
	}
	return map[string]any{
		"id":         e.ID,
		"run_id":     e.RunID,
		"event_type": e.EventType,
		"task_id":    e.TaskID,
		"backend":    e.Backend,
		"ts":         e.TS,
		"extra":      extra,
	}
}

func extraString(extra map[string]any, key string) string {
	if s, ok := extra[key].(string); ok {
		return s
	}
	return ""
}

// extraRaw renders a value the way Python's f-string would. Attempt counters
// arrive as JSON numbers, so `2` must print as "2" and never as "2.000000".
func extraRaw(extra map[string]any, key string) string {
	v, ok := extra[key]
	if !ok || v == nil {
		return ""
	}
	switch n := v.(type) {
	case float64:
		if n == float64(int64(n)) {
			return strconv.FormatInt(int64(n), 10)
		}
		return strconv.FormatFloat(n, 'g', -1, 64)
	case string:
		return n
	case bool:
		if n {
			return "True"
		}
		return "False"
	default:
		return fmt.Sprint(v)
	}
}

func extraInt(extra map[string]any, key string) (int, bool) {
	f, ok := toFloat(extra[key])
	if !ok {
		return 0, false
	}
	return int(f), true
}

func extraFloat(extra map[string]any, key string) (float64, bool) {
	return toFloat(extra[key])
}

// toFloat accepts the forms a JSON `extra` can hold, including the numeric
// string — Python's `float(x)` takes those too, and the event log has them.
func toFloat(raw any) (float64, bool) {
	switch v := raw.(type) {
	case nil:
		return 0, false
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case string:
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}
