// Command orch walks a tasks.json DAG and dispatches each task to a local
// AI CLI (claude | codex | opencode | gemini | agy). See CLAUDE.md's
// "Migración a Go" section for the target layout; the cobra wiring itself
// lives in internal/cli so nothing under internal/ ever imports cmd/.
package main

import (
	"os"

	"github.com/hectorcanaimero/orch/internal/cli"
)

// version is overridden at build time via -ldflags "-X main.version=...".
// The Makefile fills it in from `git describe --tags --always`; a plain
// `go build` (no ldflags) keeps the "dev" fallback.
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:]))
}

// run executes the CLI against args and returns the process exit code,
// without calling os.Exit itself — kept separate from main so tests can
// drive it in-process.
func run(args []string) int {
	return cli.Run(version, args)
}
