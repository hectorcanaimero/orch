// Command sitegen writes the site pages built from orch's own data: the
// template gallery (site/templates) and the playground page (site/playground). Run it from the repository
// root: `make site-templates`.
package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// siteDir is the public site, relative to the repository root.
const siteDir = "site"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "sitegen:", err)
		os.Exit(1)
	}
}

func run() error {
	pages, err := TemplatePages()
	if err != nil {
		return err
	}
	for name, body := range pages {
		path := filepath.Join(siteDir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { // #nosec G301 -- public static site directory
			return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, body, 0o644); err != nil { // #nosec G306 -- public static site files
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	return nil
}
