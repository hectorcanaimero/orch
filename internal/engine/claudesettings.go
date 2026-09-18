package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// claudeSettingsFiles are the project-level settings claude loads from its
// working directory, lowest precedence first.
var claudeSettingsFiles = []string{"settings.json", "settings.local.json"}

// projectClaudeSettings returns the project root's claude settings that a
// worktree does not have, merged into one JSON document for `--settings`.
//
// A repo that gitignores .claude/ (settings.local.json is ignored by default)
// checks out worktrees without them, so an agent there ran with no
// allow-list: under acceptEdits every Bash, web and MCP call was refused and
// the run still succeeded (#231). A file the worktree already has is left
// out — claude loads it itself, and passing it again would run its hooks
// twice. Empty when there is nothing to add or the dispatch is in the root.
func projectClaudeSettings(root, workdir string) string {
	if root == "" || filepath.Clean(root) == filepath.Clean(workdir) {
		return ""
	}
	var merged map[string]any
	for _, name := range claudeSettingsFiles {
		if _, err := os.Stat(filepath.Join(workdir, ".claude", name)); err == nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(root, ".claude", name)) // #nosec G304 -- the project's own settings file
		if err != nil {
			continue
		}
		var doc map[string]any
		if json.Unmarshal(raw, &doc) != nil {
			continue // claude would refuse it too; not orch's to fix
		}
		merged = mergeSettings(merged, doc)
	}
	if merged == nil {
		return ""
	}
	out, err := json.Marshal(merged)
	if err != nil {
		return ""
	}
	return string(out)
}

// mergeSettings layers src over dst the way claude layers settings files:
// objects merge key by key, lists (permission rules) accumulate, and any
// other value from the later file wins.
func mergeSettings(dst, src map[string]any) map[string]any {
	if dst == nil {
		dst = map[string]any{}
	}
	for k, v := range src {
		switch sv := v.(type) {
		case map[string]any:
			dv, _ := dst[k].(map[string]any)
			dst[k] = mergeSettings(dv, sv)
		case []any:
			dv, _ := dst[k].([]any)
			// Start from a non-nil slice: append(nil, empty...) is nil, which
			// marshals to null, and claude drops a settings document whose
			// "deny": null fails its schema — allow rules and all.
			dst[k] = append(append([]any{}, dv...), sv...)
		default:
			dst[k] = v
		}
	}
	return dst
}
