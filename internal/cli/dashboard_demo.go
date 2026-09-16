package cli

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/demo"
)

// prepareDemo builds the synthetic project `orch dashboard --demo` serves and
// points the command's project flags at it. The returned cleanup removes the
// temporary directory; the caller defers it.
//
// A temp directory, never the working directory: the demo writes a
// tasks.json, scripts and a database, and none of that belongs in whatever
// folder the operator happened to be in.
func prepareDemo(cmd *cobra.Command, flags *projectFlags, withTunnel bool, portfolio string) (func(), error) {
	switch {
	case withTunnel:
		return nil, errors.New("--demo cannot be combined with --tunnel: demo data is not something to publish")
	case portfolio != "":
		return nil, errors.New("--demo cannot be combined with --portfolio")
	case flags.root != "" || flags.id != "" || flags.configPath != "":
		return nil, errors.New("--demo builds its own project; drop --project-root, --project-id and --config")
	}

	dir, err := os.MkdirTemp("", "orch-demo-")
	if err != nil {
		return nil, fmt.Errorf("create the demo directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	paths, err := demo.Build(cmd.Context(), dir, time.Now())
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("build the demo project: %w", err)
	}
	flags.root, flags.id = paths.Root, paths.ID

	_, _ = fmt.Fprintf(cmd.OutOrStdout(),
		"DEMO DATA — %s is a synthetic project. Nothing is dispatched and no provider is called.\n"+
			"It lives in %s and is deleted when the dashboard stops.\n\n", demo.Name, dir)
	return cleanup, nil
}
