package state

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// Event and spend rows are inserted with `INSERT OR IGNORE` against a UNIQUE
// dedup_hash, so replaying a run — after a crash, a resume, or a second
// orch attached to the same project — does not double-count.
//
// The hash is part of the on-disk contract, not an implementation detail: a
// Go binary and a Python binary can be pointed at the same orch.db, and if
// they disagree about the preimage the deduplication silently stops working.
// The preimages below are copied from
// `orchestrator/state/sqlite_backend.py` (`append_event`, `append_spend`);
// `testdata/README.md` carries the golden vectors Python actually produced.

// eventDedupHash mirrors sqlite_backend.append_event.
//
//	sha256("{project_id}|{ts}|{task_id}|{event_type}|{run_id}|{pid_hint}")
//
// pidHint is `extra["pid"]` rendered as Python would render it, or the empty
// string when the key is absent — note that the separator is still written,
// so an event with no pid hashes a trailing "|".
func eventDedupHash(projectID, ts, taskID, eventType, runID, pidHint string) string {
	return sha256Hex(strings.Join(
		[]string{projectID, ts, taskID, eventType, runID, pidHint}, "|",
	))
}

// spendDedupHash mirrors sqlite_backend.append_spend.
//
//	sha256("{project_id}|{ts}|{task_id}|{backend}|{model}|{cost_usd}|{duration_s}")
//
// costUSD and durationS go through pyFloat because Python interpolates them
// with an f-string. See pyfloat.go for why that matters.
func spendDedupHash(projectID, ts, taskID, backend, model string, costUSD, durationS float64) string {
	return sha256Hex(strings.Join([]string{
		projectID, ts, taskID, backend, model,
		pyFloat(costUSD), pyFloat(durationS),
	}, "|"))
}

// pidHintFromExtra reproduces Python's `(entry.extra or {}).get("pid", "")`
// followed by f-string interpolation.
//
// The value arrives from JSON, where it may be a float64 (encoding/json's
// default for numbers), an int, or a string. Python holds an int there and
// renders it without a decimal point, so an integral float64 must render the
// same way — `4240`, never `4240.0`. A non-integral number is rendered by
// pyFloat, which is what Python would do if something ever put one there.
func pidHintFromExtra(extra map[string]any) string {
	v, ok := extra["pid"]
	if !ok || v == nil {
		return ""
	}
	switch n := v.(type) {
	case string:
		return n
	case int:
		return pyInt(int64(n))
	case int64:
		return pyInt(n)
	case float64:
		if n == float64(int64(n)) {
			return pyInt(int64(n))
		}
		return pyFloat(n)
	default:
		return fmt.Sprint(v)
	}
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
