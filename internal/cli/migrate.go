package cli

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/state"
)

// newMigrateCmd ports `orch migrate` (orchestrator/migrate.py): a one-shot
// importer of a file-mode project's history — tasks.json's per-task status,
// state/events-*.jsonl, state/spend-*.jsonl — into orch.db. Deprecated from
// day one: the file backend it reads from has no Go implementation and none
// is planned (SQLite has been the only writer since v0.11); this command
// exists only so a project someone never migrated under the Python binary
// still has a way in.
//
// Deliberately narrower than migrate.py in two ways, both explained in
// docs/brainstorm/go-migration-notes.md:
//
//   - state/run-*.json (runs + in-flight dispatches) is not imported.
//     Backend has no method shaped for historical run data — StartRun/
//     RecordDispatch write a LIVE run's current state, not an arbitrary
//     past one with its own completed/blocked/deferred lists — and
//     neither `orch status` nor `orch tasks` reads the runs/dispatches
//     tables at all, so skipping it cannot affect this command's own
//     acceptance check (a migrated project's `status --json` matching
//     Python's).
//   - --rollback/--backup-dir/--sqlite-path/--from are not wired. The
//     backup step still runs unconditionally (there is no flag to change
//     where it goes or skip it), but restoring from one is a manual
//     `cp -r` today rather than a flag.
func newMigrateCmd(flags *projectFlags) *cobra.Command {
	var (
		dryRun bool
		force  bool
	)
	cmd := &cobra.Command{
		Use:        "migrate",
		Short:      "One-shot import of a file-mode project's history into orch.db",
		Deprecated: "the file backend is legacy — every project should already be on sqlite",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			paths, err := resolveAndValidate(flags)
			if err != nil {
				return withExitCode(2, err)
			}
			cfg, err := loadConfigTolerantOfFileBackend(paths)
			if err != nil {
				return fmt.Errorf("config load failed: %w", err)
			}

			if dryRun {
				report, err := planMigration(paths)
				if err != nil {
					return withExitCode(1, err)
				}
				return report.printDryRun(cmd.OutOrStdout())
			}

			report, err := runMigration(ctx, paths, cfg, force)
			if err != nil {
				return err
			}
			return report.print(cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Log what would be imported; touch nothing")
	cmd.Flags().BoolVar(&force, "force", false, "Re-import even if this project was already migrated")
	return cmd
}

// ---- dry-run --------------------------------------------------------------

type dryRunReport struct {
	projectID  string
	stateDir   string
	sqlitePath string
	runFiles   int
	eventFiles int
	eventRows  int
	spendFiles int
	spendRows  int
}

func planMigration(paths config.Paths) (*dryRunReport, error) {
	stateDir := resolveSourceStateDir(paths)
	if err := checkSourceLayout(paths, stateDir); err != nil {
		return nil, err
	}
	// Glob's only error is a malformed pattern (ErrBadPattern), which these
	// fixed literals can never produce — same reasoning as
	// config.hasNamespacedState's own comment on the same call.
	runFiles, _ := filepath.Glob(filepath.Join(stateDir, "run-*.json"))
	eventFiles, _ := filepath.Glob(filepath.Join(stateDir, "events-*.jsonl"))
	spendFiles, _ := filepath.Glob(filepath.Join(stateDir, "spend-*.jsonl"))

	eventRows := 0
	for _, f := range eventFiles {
		rows, _, err := readJSONLDicts(f)
		if err != nil {
			return nil, err
		}
		eventRows += len(rows)
	}
	spendRows := 0
	for _, f := range spendFiles {
		rows, _, err := readJSONLDicts(f)
		if err != nil {
			return nil, err
		}
		spendRows += len(rows)
	}

	return &dryRunReport{
		projectID: paths.ID, stateDir: stateDir,
		sqlitePath: filepath.Join(stateDir, "orch.db"),
		runFiles:   len(runFiles), eventFiles: len(eventFiles), eventRows: eventRows,
		spendFiles: len(spendFiles), spendRows: spendRows,
	}, nil
}

func (r *dryRunReport) printDryRun(w io.Writer) error {
	_, err := fmt.Fprintf(w,
		"migrate (dry-run):\n"+
			"  project_id  : %s\n"+
			"  state_dir   : %s\n"+
			"  sqlite_path : %s\n"+
			"  runs        : %d files (not imported — see --help)\n"+
			"  events      : %d rows across %d files\n"+
			"  spend       : %d rows across %d files\n",
		r.projectID, r.stateDir, r.sqlitePath,
		r.runFiles, r.eventRows, r.eventFiles, r.spendRows, r.spendFiles)
	return err
}

// loadConfigTolerantOfFileBackend loads config.yaml, treating
// config.ErrFileBackend as expected rather than fatal: that error's own
// message says "Import the existing state with `orch migrate`", so this
// command — and only this command — is allowed to see it and proceed with
// defaults, exactly the case ErrFileBackend exists to describe. Every
// other project-scoped command still treats it as a hard stop, unchanged.
func loadConfigTolerantOfFileBackend(paths config.Paths) (config.Config, error) {
	res, err := config.Load(paths.ConfigYAML, paths.Root)
	if err == nil {
		return res.Config, nil
	}
	if errors.Is(err, config.ErrFileBackend) {
		return config.Defaults(), nil
	}
	return config.Config{}, err
}

// checkSourceLayout mirrors migrate.py's two preconditions: tasks.json and
// the state directory must both exist. Exit code 1 (an I/O/layout problem),
// not 2 (which resolveAndValidate already used for "not an orch project at
// all").
func checkSourceLayout(paths config.Paths, stateDir string) error {
	if _, err := os.Stat(paths.TasksJSON()); err != nil {
		return withExitCode(1, fmt.Errorf("migrate: tasks.json not found at %s", paths.TasksJSON()))
	}
	if _, err := os.Stat(stateDir); err != nil {
		return withExitCode(1, fmt.Errorf("migrate: state dir not found at %s", stateDir))
	}
	return nil
}

// resolveSourceStateDir ports migrate.py's own legacy-path fallback: if
// paths.StateDir() (the NAMESPACED `.orchestrator/state/<id>/` a command
// invoked with an explicit --project-root normally resolves to) is empty
// of state files, but its PARENT — the flat `.orchestrator/state/` layout
// every project used before namespacing existed — has some, migrate reads
// from there instead.
//
// This is deliberately re-derived here rather than reused from
// config.Paths: ResolvePaths' own namespace detection
// (hasNamespacedState) only ever flips LEGACY to NAMESPACED when it finds
// evidence a project already has namespaced state; it has no reason to
// flip the other way, because every OTHER command wants the namespaced
// path to exist even when empty (that's where it will write). `migrate`
// is the one command reading a project that may predate namespacing
// entirely, so it alone needs the fallback — matching the shape of
// migrate.py's own state_dir/has_state_files check exactly.
func resolveSourceStateDir(paths config.Paths) string {
	stateDir := paths.StateDir()
	if hasStateFiles(stateDir) || paths.Layout != config.LayoutNamespaced {
		return stateDir
	}
	if legacy := filepath.Dir(stateDir); hasStateFiles(legacy) {
		return legacy
	}
	return stateDir
}

// hasStateFiles mirrors migrate.py's own check: at least one run-*.json or
// events-*.jsonl file. spend-*.jsonl is deliberately not part of the test —
// same as Python, which never checks it either.
func hasStateFiles(dir string) bool {
	if _, err := os.Stat(dir); err != nil {
		return false
	}
	// Glob's only error is a malformed pattern, which these fixed literals
	// can never produce.
	runs, _ := filepath.Glob(filepath.Join(dir, "run-*.json"))
	events, _ := filepath.Glob(filepath.Join(dir, "events-*.jsonl"))
	return len(runs) > 0 || len(events) > 0
}

// ---- real import ------------------------------------------------------

type migrateReport struct {
	tasksImported  int
	eventsImported int
	eventsSkipped  map[string]int // unknown event_type -> count
	spendImported  int
	malformedLines int
	backupPath     string
	sqlitePath     string
}

func (r *migrateReport) print(w io.Writer) error {
	if _, err := fmt.Fprintf(w,
		"migrate: done\n"+
			"  tasks_runtime : %d\n"+
			"  events        : %d\n"+
			"  spend         : %d\n"+
			"  backup        : %s\n"+
			"  sqlite_path   : %s\n",
		r.tasksImported, r.eventsImported, r.spendImported, r.backupPath, r.sqlitePath,
	); err != nil {
		return err
	}
	if r.malformedLines > 0 {
		if _, err := fmt.Fprintf(w, "  skipped %d malformed JSONL line(s)\n", r.malformedLines); err != nil {
			return err
		}
	}
	if len(r.eventsSkipped) > 0 {
		types := make([]string, 0, len(r.eventsSkipped))
		for t := range r.eventsSkipped {
			types = append(types, t)
		}
		sort.Strings(types)
		for _, t := range types {
			if _, err := fmt.Fprintf(w, "  skipped %d event(s) of unknown type %q\n", r.eventsSkipped[t], t); err != nil {
				return err
			}
		}
	}
	return nil
}

func runMigration(ctx context.Context, paths config.Paths, cfg config.Config, force bool) (report *migrateReport, err error) {
	stateDir := resolveSourceStateDir(paths)
	if err := checkSourceLayout(paths, stateDir); err != nil {
		return nil, err
	}
	sqlitePath := paths.SQLitePath(cfg)

	// Bootstrap the DB first (creates the schema on a first run) — the
	// migrated_at guard below needs the `projects` table to already exist,
	// same ordering Python uses (SqliteBackend() runs before its own
	// conn_check).
	db, _, err := state.Open(ctx, sqlitePath)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", sqlitePath, err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("close %s: %w", sqlitePath, cerr))
		}
	}()

	if !force {
		at, err := migratedAt(sqlitePath, paths.ID)
		if err != nil {
			return nil, fmt.Errorf("check migrated_at: %w", err)
		}
		if at != "" {
			return nil, withExitCode(1, fmt.Errorf(
				"migrate: project %q already migrated at %s (use --force to re-import)", paths.ID, at))
		}
	}

	backupPath, err := backupStateDir(stateDir)
	if err != nil {
		return nil, withExitCode(1, fmt.Errorf("migrate: backup failed: %w", err))
	}

	backend := state.NewSQLite(db, paths.ID, paths.Root)
	tasks := loadDAG(paths)
	if err := backend.Bootstrap(ctx, tasks); err != nil {
		return nil, fmt.Errorf("migrate: bootstrap: %w", err)
	}

	nEvents, skipped, malformed1, err := importEvents(ctx, backend, stateDir)
	if err != nil {
		return nil, fmt.Errorf("migrate: import events: %w", err)
	}
	nSpend, malformed2, err := importSpend(ctx, backend, stateDir)
	if err != nil {
		return nil, fmt.Errorf("migrate: import spend: %w", err)
	}

	if err := markMigrated(ctx, sqlitePath, paths.ID, utcNowISO()); err != nil {
		return nil, fmt.Errorf("migrate: mark migrated: %w", err)
	}

	return &migrateReport{
		tasksImported: len(tasks), eventsImported: nEvents, eventsSkipped: skipped,
		spendImported: nSpend, malformedLines: malformed1 + malformed2,
		backupPath: backupPath, sqlitePath: sqlitePath,
	}, nil
}

// ---- events / spend import ----------------------------------------------

func importEvents(ctx context.Context, backend state.Backend, stateDir string) (imported int, skipped map[string]int, malformed int, err error) {
	skipped = map[string]int{}
	files, globErr := filepath.Glob(filepath.Join(stateDir, "events-*.jsonl"))
	if globErr != nil {
		return 0, skipped, 0, globErr
	}
	sort.Strings(files)
	for _, path := range files {
		runID := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "events-"), ".jsonl")
		rows, bad, rerr := readJSONLDicts(path)
		malformed += bad
		if rerr != nil {
			// A whole file that can't even be opened is a warning in
			// Python, not a fatal error — the rest of the migration still
			// runs. Matched here: skip the file, keep going.
			continue
		}
		for _, row := range rows {
			extra, _ := row["extra"].(map[string]any)
			e := state.Event{
				EventType: strOr(row["event_type"], "dispatch"),
				TaskID:    strOr(row["task_id"], "-"),
				Backend:   strOr(row["backend"], ""),
				TS:        strOr(row["ts"], utcNowISO()),
				Extra:     extra,
			}
			if aerr := backend.AppendEvent(ctx, runID, e); aerr != nil {
				if errors.Is(aerr, state.ErrUnknownEventType) {
					skipped[e.EventType]++
					continue
				}
				return imported, skipped, malformed, fmt.Errorf("event for %q: %w", e.TaskID, aerr)
			}
			imported++
		}
	}
	return imported, skipped, malformed, nil
}

func importSpend(ctx context.Context, backend state.Backend, stateDir string) (imported, malformed int, err error) {
	files, globErr := filepath.Glob(filepath.Join(stateDir, "spend-*.jsonl"))
	if globErr != nil {
		return 0, 0, globErr
	}
	sort.Strings(files)
	for _, path := range files {
		rows, bad, rerr := readJSONLDicts(path)
		malformed += bad
		if rerr != nil {
			continue
		}
		for _, row := range rows {
			s := state.Spend{
				ProjectID: strOr(row["project_id"], ""),
				TS:        strOr(row["ts"], utcNowISO()),
				TaskID:    strOr(row["task_id"], "-"),
				Backend:   strOr(row["backend"], ""),
				Model:     strOr(row["model"], ""),
				TokensIn:  intOr(row["tokens_in"], 0),
				TokensOut: intOr(row["tokens_out"], 0),
				CostUSD:   floatOr(row["cost_usd"], 0),
				DurationS: floatOr(row["duration_s"], 0),
			}
			if serr := backend.RecordSpend(ctx, s); serr != nil {
				return imported, malformed, fmt.Errorf("spend for %q: %w", s.TaskID, serr)
			}
			imported++
		}
	}
	return imported, malformed, nil
}

// readJSONLDicts reads one JSONL file into generic rows, silently skipping
// (and counting) any line that isn't valid JSON or isn't a JSON object —
// ports _iter_jsonl_dicts's tolerance, minus the per-line WARN log (the
// caller reports a single count instead).
func readJSONLDicts(path string) (rows []map[string]any, malformed int, err error) {
	f, ferr := os.Open(path) // #nosec G304 -- path comes from this package's own filepath.Glob over the project's state dir
	if ferr != nil {
		return nil, 0, ferr
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("close %s: %w", path, cerr))
		}
	}()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var obj map[string]any
		if jerr := json.Unmarshal([]byte(line), &obj); jerr != nil {
			malformed++
			continue
		}
		rows = append(rows, obj)
	}
	if err := scanner.Err(); err != nil {
		return rows, malformed, err
	}
	return rows, malformed, nil
}

func strOr(v any, def string) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return def
}

func floatOr(v any, def float64) float64 {
	if f, ok := v.(float64); ok {
		return f
	}
	return def
}

func intOr(v any, def int) int {
	if f, ok := v.(float64); ok { // encoding/json decodes every JSON number as float64
		return int(f)
	}
	return def
}

func utcNowISO() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05Z")
}

// ---- migrated_at guard --------------------------------------------------
//
// A raw connection, deliberately outside the Backend abstraction — Python's
// own migrate.py does the same (a bare sqlite3.connect() just for this
// check), because "has this project been migrated" isn't a query any other
// caller needs and doesn't belong on the Backend interface.

func migratedAt(dbPath, projectID string) (at string, err error) {
	db, oerr := sql.Open("sqlite", dbPath)
	if oerr != nil {
		return "", oerr
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("close %s: %w", dbPath, cerr))
		}
	}()

	var v sql.NullString
	qerr := db.QueryRow("SELECT migrated_at FROM projects WHERE project_id = ?", projectID).Scan(&v)
	switch {
	case errors.Is(qerr, sql.ErrNoRows):
		return "", nil
	case qerr != nil:
		return "", qerr
	case !v.Valid:
		return "", nil
	default:
		return v.String, nil
	}
}

func markMigrated(ctx context.Context, dbPath, projectID, ts string) (err error) {
	db, oerr := sql.Open("sqlite", dbPath)
	if oerr != nil {
		return oerr
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("close %s: %w", dbPath, cerr))
		}
	}()

	_, err = db.ExecContext(ctx, "UPDATE projects SET migrated_at = ? WHERE project_id = ?", ts, projectID)
	return err
}

// ---- backup ---------------------------------------------------------------

// backupStateDir copies stateDir into a timestamped directory under
// `<stateDir>-backups/`, next to it (not inside it, so the copy can never
// recurse into itself), skipping the sqlite DB files and any prior backup
// directories. Ports migrate.py's _make_backup, minus symlink preservation
// (state dirs are plain files in every project seen in the wild — a
// worthwhile simplification for a one-shot, deprecated-on-day-one command).
func backupStateDir(stateDir string) (string, error) {
	backupRoot := filepath.Join(filepath.Dir(stateDir), filepath.Base(stateDir)+"-backups")
	if err := os.MkdirAll(backupRoot, 0o750); err != nil {
		return "", fmt.Errorf("create %s: %w", backupRoot, err)
	}

	base := time.Now().UTC().Format("20060102T150405Z") + "-" + filepath.Base(stateDir)
	dest := filepath.Join(backupRoot, base)
	for seq := 1; ; seq++ {
		if _, err := os.Stat(dest); os.IsNotExist(err) {
			break
		}
		dest = filepath.Join(backupRoot, fmt.Sprintf("%s-%d", base, seq))
	}

	skip := func(name string) bool {
		return name == "orch.db" || strings.HasSuffix(name, ".db-wal") ||
			strings.HasSuffix(name, ".db-shm") || name == "backups" ||
			strings.HasSuffix(name, "-backups")
	}
	if err := copyDir(stateDir, dest, skip); err != nil {
		return "", err
	}
	return dest, nil
}

func copyDir(src, dst string, skip func(name string) bool) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	if err := os.MkdirAll(dst, 0o750); err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	for _, e := range entries {
		if skip(e.Name()) {
			continue
		}
		srcPath := filepath.Join(src, e.Name())
		dstPath := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := copyDir(srcPath, dstPath, skip); err != nil {
				return err
			}
			continue
		}
		if err := copyFile(srcPath, dstPath); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) (err error) {
	in, ierr := os.Open(src) // #nosec G304 -- src is this package's own recursive walk of the project's state dir
	if ierr != nil {
		return fmt.Errorf("open %s: %w", src, ierr)
	}
	defer func() {
		if cerr := in.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("close %s: %w", src, cerr))
		}
	}()

	out, oerr := os.Create(dst) // #nosec G304 -- dst is this package's own backup destination under the resolved backup root
	if oerr != nil {
		return fmt.Errorf("create %s: %w", dst, oerr)
	}
	defer func() {
		if cerr := out.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("close %s: %w", dst, cerr))
		}
	}()

	if _, cerr := io.Copy(out, in); cerr != nil {
		return fmt.Errorf("copy %s to %s: %w", src, dst, cerr)
	}
	return nil
}
