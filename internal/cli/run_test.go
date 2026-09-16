package cli

import (
	"testing"

	"github.com/hectorcanaimero/orch/internal/config"
)

// budget.per_dispatch_usd is documented (docs/CONFIG.md, the packaged
// config.yaml) as the cap handed to `claude --max-budget-usd`, but `orch run`
// never set SchedulerOptions.BudgetUSD, so the flag was never passed. Zero or
// less means no cap, the same convention the escalation check uses.
func TestPerDispatchCap(t *testing.T) {
	cfg := config.Defaults()
	if got := perDispatchCap(cfg); got == nil || *got != 5.0 {
		t.Errorf("default config: cap = %v, want 5.0", got)
	}
	cfg.Budget.PerDispatchUSD = 0
	if got := perDispatchCap(cfg); got != nil {
		t.Errorf("per_dispatch_usd 0: cap = %v, want nil (no --max-budget-usd)", *got)
	}
}
