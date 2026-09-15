package doctor

import (
	"fmt"
	"strings"

	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/router"
)

// CheckRoutes reports whether every task's model resolves to a route in r.
// Ports the models.resolve check from build_doctor_report
// (orchestrator/doctor.py), via router.Router.Validate rather than
// reimplementing the resolution loop.
func CheckRoutes(tasks []model.Task, r router.Router) Check {
	const name = "models.resolve"
	if len(tasks) == 0 {
		return Check{Name: name, Status: StatusSkip, Detail: "no tasks loaded — cannot check resolution"}
	}
	if len(r) == 0 {
		return Check{Name: name, Status: StatusSkip, Detail: "router did not load — cannot check resolution"}
	}
	if err := r.Validate(tasks); err != nil {
		return Check{
			Name: name, Status: StatusError,
			Detail:      err.Error(),
			Remediation: "Add the missing entries to model_router.yaml.",
		}
	}
	// #266: resolving is not enough when the route names a model the CLI
	// rejects — every dispatch would 404. Offline and a warning only; see
	// router.CLIModelWarning.
	if warnings := r.CLIModelWarnings(tasks); len(warnings) > 0 {
		return Check{
			Name: name, Status: StatusWarn,
			Detail: fmt.Sprintf("%d task(s) resolve, but %s",
				len(tasks), strings.Join(warnings, "; ")),
			Remediation: "Fix the cli_model of each route named above in model_router.yaml.",
		}
	}
	return Check{Name: name, Status: StatusOK, Detail: fmt.Sprintf("%d task(s) resolve", len(tasks))}
}
