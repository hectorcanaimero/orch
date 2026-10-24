package engine

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hectorcanaimero/orch/internal/budget"
	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/providers"
	"github.com/hectorcanaimero/orch/internal/state"
)

// A one-shot on a provider over its budget window is refused before it
// spawns, so it spends nothing more (#461).
func TestOneShotRespectsTheBudgetGate(t *testing.T) {
	ctx := context.Background()
	b := openBackend(t)
	if err := b.RecordSpend(ctx, state.Spend{TS: utcSecond(time.Now()), TaskID: "B-020", Backend: "claude", TokensIn: 100, Attempt: 1}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(FakeProviderEnv, fakeDir(t, model.BackendClaude, "summary-run-1",
		`{"type":"result","subtype":"success","is_error":false,"result":"ok","usage":{"input_tokens":1,"output_tokens":1}}`, 0, ""))
	p, err := providers.Get(model.BackendClaude)
	if err != nil {
		t.Fatal(err)
	}
	gate := budget.NewGate(b, &budget.Config{Providers: map[string]budget.ProviderBudget{
		"claude": {WindowHours: 5, TokenBudget: 10, ThresholdPct: 100},
	}})
	dir := t.TempDir()
	_, err = OneShot{State: b, Gate: gate}.Run(ctx, Dispatch{
		Provider:   p,
		Req:        providers.Request{TaskID: "summary-run-1", Route: model.RouteEntry{Backend: model.BackendClaude, CLIModel: "haiku"}},
		PromptPath: promptFile(t, "explain"),
		LogPath:    filepath.Join(dir, "summary.log"),
		Cwd:        dir,
	})
	if err == nil || !strings.Contains(err.Error(), "budget") {
		t.Errorf("Run = %v, want a budget refusal", err)
	}
	rows, err := b.(*state.SQLite).AllSpend(ctx, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Errorf("%d spend rows, want only the one already there", len(rows))
	}
}
