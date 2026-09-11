package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/config"
)

// newConfigCmd is the `orch config` parent. Python's `_run_config_subcommand`
// only implements `consolidate` (H-2, folding a project-root dashboard.yaml
// into config.yaml) — `show` has no Python counterpart. `consolidate` isn't
// ported here; this parent exists so `show` sits under the same `orch
// config <verb>` shape Python already has, ready for `consolidate` to join
// it later without a breaking CLI change.
func newConfigCmd(flags *projectFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Config helpers",
	}
	cmd.AddCommand(newConfigShowCmd(flags))
	return cmd
}

// newConfigShowCmd is `orch config show` — new in Go, a thin wrapper over
// the already-existing config.Show (its own doc comment: "`orch config
// show` is a three-line command on top of this"). Prints the effective
// config — defaults, file, and any dashboard.yaml/H-2 override merged — as
// YAML, followed by the sources it came from and any ignored keys. The
// point is answering "why is orch doing that?": a dump with no sources
// sends an operator looking in the wrong file.
func newConfigShowCmd(flags *projectFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Print the effective config, merged with defaults, and where each value came from",
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := resolveAndValidate(flags)
			if err != nil {
				return err
			}
			res, err := config.Load(paths.ConfigYAML, paths.Root)
			if err != nil {
				return fmt.Errorf("config load failed: %w", err)
			}
			return config.Show(cmd.OutOrStdout(), res)
		},
	}
	return cmd
}
