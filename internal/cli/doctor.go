package cli

import (
	"context"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/budget"
	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/doctor"
	"github.com/hectorcanaimero/orch/internal/router"
	"github.com/hectorcanaimero/orch/internal/tunnel"
)

// doctorProjectJSON / doctorSummaryJSON / doctorPayload mirror
// orchestrator/doctor.py::build_doctor_report's JSON shape field-for-field
// (see internal/doctor's Check for why its own json tags already match
// CheckResult.as_json()) — a caller consuming `orch doctor --json` sees
// the same {project, backend, checks, summary, exit_code} envelope either
// binary produces, for the checks Go actually runs (see newDoctorCmd's
// doc comment for the narrower set).
type doctorProjectJSON struct {
	ID   string `json:"id"`
	Root string `json:"root"`
}

type doctorSummaryJSON struct {
	OK    int `json:"ok"`
	Warn  int `json:"warn"`
	Error int `json:"error"`
	Skip  int `json:"skip"`
}

type doctorPayload struct {
	Project  doctorProjectJSON `json:"project"`
	Backend  string            `json:"backend"`
	Checks   []doctor.Check    `json:"checks"`
	Summary  doctorSummaryJSON `json:"summary"`
	ExitCode int               `json:"exit_code"`
}

// newDoctorCmd wires internal/doctor's checks (G4.5) into a subcommand —
// the package itself has no CLI surface; this is that surface.
//
// Runs the seven Check* functions internal/doctor exports: backends
// (referenced by tasks.json + model_router.yaml), routes (routing
// completeness), budget preset, orphaned worktrees, VCS readiness, MCP
// config, and SQLite health. This is the narrower G4.5 cut documented in
// internal/doctor's own package comment — Python's build_doctor_report
// also runs check_config_files and check_scripts, which already have Go
// homes elsewhere (`orch validate`'s config/router/tasks load checks,
// resolveAndValidate's tasks.json+task-start.sh check) rather than being
// duplicated here.
//
// Tolerant like `orch validate`, not strict like most project-scoped
// commands: a project missing tasks.json or config.yaml is exactly the
// case this command exists to diagnose, so a load failure becomes a
// `config.parse`/`tasks.parse`-shaped Check, not a CLI error that exits
// before any check runs.
func newDoctorCmd(flags *projectFlags) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Environment preflight — provider CLIs, VCS, worktrees, budget config, SQLite",
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := config.ResolvePaths(flags.root, flags.id, flags.configPath)
			if err != nil {
				return withExitCode(2, err)
			}

			var checks []doctor.Check
			cfg := config.Defaults()
			if res, cfgErr := config.Load(paths.ConfigYAML, paths.Root); cfgErr == nil {
				cfg = res.Config
			} else {
				checks = append(checks, doctor.Check{
					Name: "config.parse", Status: doctor.StatusError,
					Detail: fmt.Sprintf("config load failed: %v", cfgErr),
				})
			}

			tasks := loadDAG(paths)

			var rtr router.Router
			if r, routerErr := router.Load(paths.RouterYAML()); routerErr == nil {
				rtr = r
			} else {
				checks = append(checks, doctor.Check{
					Name: "router.parse", Status: doctor.StatusError,
					Detail: fmt.Sprintf("router load failed: %v", routerErr),
				})
			}

			checks = append(checks, doctor.CheckRoutes(tasks, rtr))
			checks = append(checks, doctor.CheckBackends(doctor.ReferencedBackends(tasks, rtr))...)
			checks = append(checks, doctor.CheckBudgetPreset(budget.ResolvePath(paths.Root, paths.ConfigYAML, cfg.BudgetsConfig), cfg.BudgetsPreset, cfg.TypicalDispatchToken)...)
			checks = append(checks, doctor.CheckOrphanWorktrees(paths.Root))
			checks = append(checks, doctor.CheckVCSReadiness(paths.Root, doctor.VCSConfig{
				WorktreeMode: cfg.Dispatch.WorktreeMode,
				AutoPR:       cfg.VCS.AutoPR,
				Provider:     cfg.VCS.Provider,
				Host:         cfg.VCS.Host,
			})...)
			checks = append(checks, doctor.CheckMCPConfig(paths.Root))
			checks = append(checks, doctor.CheckClaudeDoneChannel(paths.Root))
			checks = append(checks, doctor.CheckTunnel(cfg.Tunnel.Enabled, tunnel.LookupBinary(""), tunnel.ConfigBlocker()))
			checks = append(checks, doctor.CheckSQLite(context.Background(), paths.SQLitePath(cfg)))

			doctor.SortByName(checks)
			payload := buildDoctorPayload(paths, cfg, checks)

			if asJSON {
				if err := printCompactJSON(cmd.OutOrStdout(), payload); err != nil {
					return err
				}
			} else if err := renderDoctorReport(cmd, payload); err != nil {
				return err
			}
			if payload.ExitCode != 0 {
				return withSilentExitCode(payload.ExitCode)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit the full doctor report as JSON on stdout")
	return cmd
}

func buildDoctorPayload(paths config.Paths, cfg config.Config, checks []doctor.Check) doctorPayload {
	if checks == nil {
		checks = []doctor.Check{}
	}
	summary := doctor.Summary(checks)
	backend := cfg.State.Backend
	if backend == "" {
		backend = "sqlite"
	}
	return doctorPayload{
		Project: doctorProjectJSON{ID: paths.ID, Root: paths.Root},
		Backend: backend,
		Checks:  checks,
		Summary: doctorSummaryJSON{
			OK: summary[doctor.StatusOK], Warn: summary[doctor.StatusWarn],
			Error: summary[doctor.StatusError], Skip: summary[doctor.StatusSkip],
		},
		ExitCode: doctor.ExitCode(checks),
	}
}

// renderDoctorReport is the human (non-JSON) renderer — plain tabular
// output, same convention as validate's: no byte-parity requirement for
// human mode.
func renderDoctorReport(cmd *cobra.Command, payload doctorPayload) error {
	out := cmd.OutOrStdout()
	if _, err := fmt.Fprintf(out, "orch doctor · project=%s · backend=%s\n",
		payload.Project.ID, payload.Backend); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "%d ok · %d warn · %d error · %d skip\n",
		payload.Summary.OK, payload.Summary.Warn, payload.Summary.Error, payload.Summary.Skip); err != nil {
		return err
	}
	if len(payload.Checks) == 0 {
		return nil
	}
	w := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "STATUS\tNAME\tDETAIL"); err != nil {
		return err
	}
	for _, c := range payload.Checks {
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\n", c.Status, c.Name, c.Detail); err != nil {
			return err
		}
	}
	return w.Flush()
}
