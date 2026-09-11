package config

import (
	"fmt"
	"io"

	"gopkg.in/yaml.v3"
)

// Show writes the effective config — defaults, file, and any override merged
// — to w as YAML, followed by the sources it came from and any warnings.
//
// Exported as a plain function rather than a cobra command so the wiring can
// land separately from the logic; `orch config show` is a three-line command
// on top of this.
//
// The point of the command is answering "why is orch doing that?" when a
// value is surprising, so it prints the SOURCES as well as the values. A
// config dump that does not say where a value came from sends you looking in
// the wrong file.
func Show(w io.Writer, res Result) error {
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	if err := enc.Encode(res.Config); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("flush config: %w", err)
	}

	if len(res.Sources) > 0 {
		if _, err := fmt.Fprintf(w, "\n# sources, in merge order:\n"); err != nil {
			return err
		}
		for _, s := range res.Sources {
			if _, err := fmt.Fprintf(w, "#   %s\n", s); err != nil {
				return err
			}
		}
	} else if _, err := fmt.Fprintf(w, "\n# no config file found — every value above is a default\n"); err != nil {
		return err
	}

	if len(res.Warnings) > 0 {
		if _, err := fmt.Fprintf(w, "\n# ignored keys:\n"); err != nil {
			return err
		}
		for _, warn := range res.Warnings {
			if _, err := fmt.Fprintf(w, "#   %s\n", warn); err != nil {
				return err
			}
		}
	}
	return nil
}
