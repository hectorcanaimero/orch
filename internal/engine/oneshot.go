package engine

import (
	"context"
	"fmt"

	"github.com/hectorcanaimero/orch/internal/budget"
	"github.com/hectorcanaimero/orch/internal/state"
)

// OneShot runs one prompt outside the task loop (#461) — `orch report
// summary --ai` today — through the same spawn and supervision as a task, so
// it honours ORCH_FAKE_PROVIDER too.
//
// The zero value only runs. With State set, the dispatch's spend row is
// written like a task attempt's, under the synthetic task id d.Req.TaskID
// (e.g. `summary-<run>`), so the cost views and the budget window see it; no
// event is written, as an outcome event would count the synthetic id as a
// task in the run's receipt. With Gate set, a provider over its budget
// window is refused before anything is spawned.
type OneShot struct {
	State state.Backend
	Gate  *budget.Gate
}

// Run checks the gate, runs d and records its spend, whether or not the
// dispatch succeeded: a failed run has usually been paid for already.
func (o OneShot) Run(ctx context.Context, d Dispatch) (Outcome, error) {
	backend := string(d.Req.Route.Backend)
	if o.Gate != nil {
		dec, err := o.Gate.CanDispatch(ctx, backend)
		if err != nil {
			return Outcome{}, fmt.Errorf("budget gate for %s: %w", backend, err)
		}
		if !dec.OK {
			return Outcome{}, fmt.Errorf("%s is over its budget window: %s", backend, dec.Reason)
		}
	}
	out, err := Run(ctx, d)
	if err != nil || o.State == nil {
		return out, err
	}
	if err := o.State.RecordSpend(ctx, spendRow(d, out, 1)); err != nil {
		return out, fmt.Errorf("record spend for %s: %w", d.Req.TaskID, err)
	}
	return out, nil
}
