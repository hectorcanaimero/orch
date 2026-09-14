package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// #231: a repo that gitignores .claude/ has no settings inside a worktree, so
// the agent ran with no allow-list and every Bash call was denied. The root's
// files are merged and handed over; one already in the worktree is left to
// claude, which loads it itself (passing it twice would run its hooks twice).
func TestProjectClaudeSettings(t *testing.T) {
	root, wt := t.TempDir(), t.TempDir()
	put := func(dir, name, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".claude", name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if got := projectClaudeSettings(root, wt); got != "" {
		t.Errorf("no settings anywhere gave %q, want nothing", got)
	}

	put(root, "settings.json", `{"permissions":{"allow":["Bash(git status)"]},"env":{"A":"1"}}`)
	put(root, "settings.local.json", `{"permissions":{"allow":["WebFetch"],"deny":["Bash(git push:*)"]}}`)

	var got map[string]any
	if err := json.Unmarshal([]byte(projectClaudeSettings(root, wt)), &got); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	perms := got["permissions"].(map[string]any)
	if allow := perms["allow"].([]any); len(allow) != 2 || allow[0] != "Bash(git status)" || allow[1] != "WebFetch" {
		t.Errorf("allow = %v, want both files' rules", allow)
	}
	if deny := perms["deny"].([]any); len(deny) != 1 {
		t.Errorf("deny = %v", deny)
	}
	if env := got["env"].(map[string]any); env["A"] != "1" {
		t.Errorf("env = %v", env)
	}

	// Tracked settings.json reaches the worktree through git: only the local
	// file is passed.
	put(wt, "settings.json", `{"permissions":{"allow":["Bash(git status)"]}}`)
	got = nil
	if err := json.Unmarshal([]byte(projectClaudeSettings(root, wt)), &got); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if allow := got["permissions"].(map[string]any)["allow"].([]any); len(allow) != 1 || allow[0] != "WebFetch" {
		t.Errorf("allow = %v, want only the local file's", allow)
	}

	// Outside a worktree claude reads the root itself.
	if got := projectClaudeSettings(root, root); got != "" {
		t.Errorf("dispatching in the root gave %q, want nothing", got)
	}
}
