package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/atomize"
)

// newAtomizeCmd ports `orch atomize` (orchestrator/atomize.py's `main`) —
// parse markdown specs and merge them into tasks.json. Read-only by
// default (prints a diff); `--apply` writes.
//
// Flags, defaults, and exit codes match Python: `--specs-dir` defaults to
// `<project-root>/docs`, `--tasks-json` to `<project-root>/tasks.json`,
// `--file` bypasses the directory walk for a single spec. Exit 2 only for
// `--file` naming a path that doesn't exist; 0 otherwise, including "no
// spec files found" and "--apply passed but nothing to apply" — Python
// treats both as successful no-ops, not errors.
//
// One thing Python's `--apply` does that this does not: after writing
// tasks.json, `main` best-effort syncs the parsed tasks into SQLite's
// `tasks_definition` table via `state_backend.upsert_task_definition`.
// `state.Backend` has no method shaped for that write yet — the same gap
// `task set --model/--backend/--milestone` already documents — so this
// step is skipped rather than faked. See docs/brainstorm/go-migration-
// notes/sonnet.md.
func newAtomizeCmd(flags *projectFlags) *cobra.Command {
	var specsDir, file, tasksJSONPath string
	var listMode, apply, noBackup bool
	cmd := &cobra.Command{
		Use:   "atomize",
		Short: "Parse markdown specs and merge them into tasks.json (read-only unless --apply)",
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := resolveAndValidate(flags)
			if err != nil {
				return err
			}

			specsRoot := filepath.Join(paths.Root, "docs")
			if specsDir != "" {
				specsRoot, err = filepath.Abs(specsDir)
				if err != nil {
					return fmt.Errorf("resolve --specs-dir: %w", err)
				}
			}
			tasksPath := paths.TasksJSON()
			if tasksJSONPath != "" {
				tasksPath, err = filepath.Abs(tasksJSONPath)
				if err != nil {
					return fmt.Errorf("resolve --tasks-json: %w", err)
				}
			}

			var specFiles []string
			if file != "" {
				abs, aerr := filepath.Abs(file)
				if aerr != nil {
					return fmt.Errorf("resolve --file: %w", aerr)
				}
				if _, serr := os.Stat(abs); serr != nil {
					return withExitCode(2, fmt.Errorf("spec file not found: %s", abs))
				}
				specFiles = []string{abs}
			} else {
				specFiles, err = atomize.WalkSpecFiles(specsRoot)
				if err != nil {
					return fmt.Errorf("walk specs under %s: %w", specsRoot, err)
				}
			}

			out := cmd.OutOrStdout()
			if len(specFiles) == 0 {
				_, err := fmt.Fprintf(out,
					"No hay specs .md bajo %s (y no se pasó --file). Nada que hacer.\n", specsRoot)
				return err
			}

			parse, err := atomize.ParseFiles(specFiles, specsRoot, paths.ID)
			if err != nil {
				return fmt.Errorf("parse specs under %s: %w", specsRoot, err)
			}

			if listMode {
				return renderAtomizeList(cmd, parse)
			}

			existing, err := atomize.LoadExisting(tasksPath)
			if err != nil {
				return fmt.Errorf("load %s: %w", tasksPath, err)
			}

			merged, diff := atomize.MergeTasks(existing, parse.Tasks)
			if _, err := fmt.Fprint(out, atomize.RenderDiff(diff, parse)); err != nil {
				return err
			}

			if !apply {
				return nil
			}
			if len(diff.NewTasks) == 0 && len(diff.Updated) == 0 {
				_, err := fmt.Fprintln(out, "--apply pasado pero no hay cambios que aplicar.")
				return err
			}

			backupPath, err := atomize.Apply(tasksPath, merged, !noBackup)
			if err != nil {
				return fmt.Errorf("write %s: %w", tasksPath, err)
			}
			if _, err := fmt.Fprintf(out, "✓ Escrito: %s\n", tasksPath); err != nil {
				return err
			}
			if backupPath != "" {
				if _, err := fmt.Fprintf(out, "  backup: %s\n", backupPath); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&specsDir, "specs-dir", "",
		"Directorio de specs .md (default: <project-root>/docs)")
	cmd.Flags().StringVar(&file, "file", "",
		"Sólo un archivo markdown (bypass del walk de --specs-dir)")
	cmd.Flags().StringVar(&tasksJSONPath, "tasks-json", "",
		"Path a tasks.json (default: <project-root>/tasks.json)")
	cmd.Flags().BoolVar(&listMode, "list", false,
		"Modo listar: sólo imprime lo parseado, sin merge/diff")
	cmd.Flags().BoolVar(&apply, "apply", false,
		"Escribí tasks.json (con backup). Sin este flag es read-only.")
	cmd.Flags().BoolVar(&noBackup, "no-backup", false,
		"No crear backup .bak-<ts> al escribir (default: sí crea)")
	return cmd
}

// renderAtomizeList is `--list`'s renderer, porting `render_list`'s content
// (scanned files, warnings, a task table) as plain tabular text rather than
// rich's colored table — same convention every human-mode renderer in this
// package follows.
func renderAtomizeList(cmd *cobra.Command, parse atomize.ParseResult) error {
	out := cmd.OutOrStdout()
	if _, err := fmt.Fprintf(out, "Specs escaneados: %d archivos\n", len(parse.FilesScanned)); err != nil {
		return err
	}
	for _, p := range parse.FilesScanned {
		if _, err := fmt.Fprintf(out, "  · %s\n", p); err != nil {
			return err
		}
	}
	if len(parse.Warnings) > 0 {
		if _, err := fmt.Fprintf(out, "Warnings: %d\n", len(parse.Warnings)); err != nil {
			return err
		}
		for _, w := range parse.Warnings {
			if _, err := fmt.Fprintf(out, "  · %s\n", w); err != nil {
				return err
			}
		}
	}
	w := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "ID\tPHASE\tTITLE\tMODEL\tEST\tDEPS"); err != nil {
		return err
	}
	for _, t := range parse.Tasks {
		model := t.Model
		if model == "" {
			model = "—"
		}
		deps := "—"
		if len(t.Dependencies) > 0 {
			deps = strings.Join(t.Dependencies, ", ")
		}
		if _, err := fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%gh\t%s\n",
			t.ID, t.Phase, t.Title, model, t.EstimateHours, deps); err != nil {
			return err
		}
	}
	return w.Flush()
}
