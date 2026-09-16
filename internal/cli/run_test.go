package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hectorcanaimero/orch/internal/config"
)

// `claude --max-budget-usd` is sent only when the project wrote
// budget.per_dispatch_usd itself. The default 5.0 keeps its older meaning —
// it limits the attempt-3 escalation — and caps nothing on its own; an
// explicit value, even one equal to the default, is a cap the operator asked
// for. Zero or less is still no cap.
func TestPerDispatchCap(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want *float64
	}{
		{"not written", "concurrency:\n  global_max: 2\n", nil},
		{"written, same as the default", "budget:\n  per_dispatch_usd: 5.0\n", ptr(5.0)},
		{"written", "budget:\n  per_dispatch_usd: 2.5\n", ptr(2.5)},
		{"written as zero", "budget:\n  per_dispatch_usd: 0\n", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "config.yaml")
			if err := os.WriteFile(path, []byte(tc.yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			res, err := config.Load(path, root)
			if err != nil {
				t.Fatal(err)
			}
			got := perDispatchCap(res.Config)
			switch {
			case tc.want == nil && got != nil:
				t.Errorf("cap = %v, want none", *got)
			case tc.want != nil && (got == nil || *got != *tc.want):
				t.Errorf("cap = %v, want %v", got, *tc.want)
			}
		})
	}
}

func ptr(v float64) *float64 { return &v }
