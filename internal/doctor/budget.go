package doctor

import (
	"fmt"

	"github.com/hectorcanaimero/orch/internal/budget"
)

// CheckBudgetPreset warns when budgetsYAML's preset has a provider window
// too small to fit two typical dispatches back to back. Ports
// validate_preset_sanity (orchestrator/preflight.py) via
// budget.LoadConfig/WarnUndersizedPresets, surfaced as Checks (doctor's own
// vocabulary) rather than Python's ValidationError (orch validate's) — this
// package is a doctor-only cut, per the G4.5 brief.
//
// budgetsYAML == "" or preset == "" means nothing is configured to check,
// matching Python's own no-op for either.
func CheckBudgetPreset(budgetsYAML, preset string, typicalDispatchTokens int) []Check {
	const name = "budget.preset_sanity"
	if budgetsYAML == "" || preset == "" {
		return []Check{{Name: name, Status: StatusSkip, Detail: "no budgets_preset configured"}}
	}

	cfg, err := budget.LoadConfig(budgetsYAML, preset)
	if err != nil {
		return []Check{{Name: name, Status: StatusError, Detail: err.Error()}}
	}
	if cfg == nil {
		return []Check{{
			Name: name, Status: StatusWarn,
			Detail: budgetsYAML + " does not exist: the budget guardrail is disabled and runs are not rationed",
			Remediation: "copy a preset file there (orch init writes one) or set budgets_config to an existing budgets.yaml",
		}}
	}

	warnings := budget.WarnUndersizedPresets(cfg, preset, typicalDispatchTokens)
	if len(warnings) == 0 {
		return []Check{{
			Name: name, Status: StatusOK,
			Detail: fmt.Sprintf("preset %q: every provider window fits >=2 typical dispatches", preset),
		}}
	}
	out := make([]Check, len(warnings))
	for i, w := range warnings {
		out[i] = Check{Name: name, Status: StatusWarn, Detail: w}
	}
	return out
}
