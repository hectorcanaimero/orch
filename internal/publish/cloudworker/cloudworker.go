// Package cloudworker embeds the orch-cloud Worker that `orch cloud setup`
// deploys, so an operator needs Node.js for wrangler but not a checkout of
// the orch-cloud repository — and so the Worker a binary deploys always
// speaks the contract version that binary's client speaks.
//
// worker.js is a build artefact of github.com/hectorcanaimero/orch-cloud,
// never edited here; README.md names the commit and the command that
// produced it.
package cloudworker

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
)

// WranglerVersion is the wrangler every `orch cloud setup` call runs
// (`npx --yes wrangler@<this>`). Pinned: a wrangler release that changes the
// deploy output or the login flow must not change what setup parses on the
// operator's machine without a PR that looks at it.
const WranglerVersion = "4.131.1"

// CompatibilityDate is the Worker's compatibility date, the same as the
// orch-cloud repository's wrangler.jsonc.
const CompatibilityDate = "2026-08-22"

// DefaultName is the Worker name when --name is not given.
const DefaultName = "orch-cloud"

// Script is the bundled Worker, byte for byte as wrangler built it.
//
//go:embed worker.js
var Script []byte

var workerName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// ValidName reports whether name is a Worker name setup accepts: lowercase
// letters, digits and dashes, starting with a letter or digit, at most 63
// characters — the part that becomes <name>.<subdomain>.workers.dev.
func ValidName(name string) error {
	if !workerName.MatchString(name) {
		return fmt.Errorf("worker name %q must be lowercase letters, digits and dashes, "+
			"start with a letter or digit, and be at most 63 characters", name)
	}
	return nil
}

// Config is the wrangler.jsonc deployed next to Script. It has no KV
// namespace id on purpose: wrangler provisions the namespace on the first
// deploy and reuses it afterwards, so a fresh machine has nothing to copy
// from the Cloudflare dashboard.
func Config(name string) ([]byte, error) {
	if err := ValidName(name); err != nil {
		return nil, err
	}
	cfg := map[string]any{
		"name":               name,
		"main":               "worker.js",
		"compatibility_date": CompatibilityDate,
		"kv_namespaces":      []map[string]string{{"binding": "ORCH"}},
		"observability":      map[string]bool{"enabled": true},
	}
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding wrangler.jsonc: %w", err)
	}
	return append(out, '\n'), nil
}
