package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// #231: a repo that gitignores .claude/ has no settings inside a worktree, so
// the agent ran with no allow-list and every Bash call was denied. The root's
// files are merged and handed over; one already in the worktree is left to
// claude, which loads it itself (passing it twice would run its hooks twice).
func TestProjectClaudeSettings(t *testing.T) {
	const (
		shared = `{"permissions":{"allow":["Bash(git status)"]},"env":{"A":"1"}}`
		local  = `{"permissions":{"allow":["WebFetch"],"deny":["Bash(git push:*)"]}}`
	)
	tests := []struct {
		name      string
		root      map[string]string // .claude/<file> in the project root
		worktree  map[string]string // .claude/<file> already in the worktree
		inRoot    bool              // dispatching in the root, not a worktree
		wantAllow []any
		wantDeny  []any
		wantEnv   map[string]any
		wantEmpty bool
	}{
		{name: "no settings anywhere", wantEmpty: true},
		{
			name:      "both root files merge, rule lists accumulate",
			root:      map[string]string{"settings.json": shared, "settings.local.json": local},
			wantAllow: []any{"Bash(git status)", "WebFetch"},
			wantDeny:  []any{"Bash(git push:*)"},
			wantEnv:   map[string]any{"A": "1"},
		},
		{
			name:      "a file the worktree tracks is not passed again",
			root:      map[string]string{"settings.json": shared, "settings.local.json": local},
			worktree:  map[string]string{"settings.json": shared},
			wantAllow: []any{"WebFetch"},
			wantDeny:  []any{"Bash(git push:*)"},
		},
		{
			name:      "dispatching in the root adds nothing",
			root:      map[string]string{"settings.json": shared},
			inRoot:    true,
			wantEmpty: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, wt := t.TempDir(), t.TempDir()
			if tt.inRoot {
				wt = root
			}
			putSettings(t, root, tt.root)
			putSettings(t, wt, tt.worktree)

			raw := projectClaudeSettings(root, wt)
			if tt.wantEmpty {
				if raw != "" {
					t.Errorf("got %q, want nothing", raw)
				}
				return
			}
			var got map[string]any
			if err := json.Unmarshal([]byte(raw), &got); err != nil {
				t.Fatalf("not JSON: %v (%q)", err, raw)
			}
			perms, _ := got["permissions"].(map[string]any)
			if !reflect.DeepEqual(perms["allow"], tt.wantAllow) {
				t.Errorf("allow = %v, want %v", perms["allow"], tt.wantAllow)
			}
			if !reflect.DeepEqual(perms["deny"], tt.wantDeny) {
				t.Errorf("deny = %v, want %v", perms["deny"], tt.wantDeny)
			}
			if env, _ := got["env"].(map[string]any); !reflect.DeepEqual(env, tt.wantEnv) {
				t.Errorf("env = %v, want %v", env, tt.wantEnv)
			}
		})
	}
}

func putSettings(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".claude", name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
