package doctor

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func writeBudgetsYAML(t *testing.T, tokenBudget int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "budgets.yaml")
	content := `
presets:
  default:
    claude:
      window_hours: 5
      token_budget: ` + strconv.Itoa(tokenBudget) + `
      threshold_pct: 90
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCheckBudgetPresetSkipsWhenNotConfigured(t *testing.T) {
	checks := CheckBudgetPreset("", "", 1000)
	if checks[0].Status != StatusSkip {
		t.Errorf("checks[0] = %+v, want skip", checks[0])
	}
}

func TestCheckBudgetPresetSkipsWhenFileMissing(t *testing.T) {
	checks := CheckBudgetPreset(filepath.Join(t.TempDir(), "missing.yaml"), "default", 1000)
	if checks[0].Status != StatusSkip {
		t.Errorf("checks[0] = %+v, want skip", checks[0])
	}
}

func TestCheckBudgetPresetOKWhenWindowFits(t *testing.T) {
	path := writeBudgetsYAML(t, 10000)
	checks := CheckBudgetPreset(path, "default", 1000)
	if checks[0].Status != StatusOK {
		t.Errorf("checks[0] = %+v, want ok", checks[0])
	}
}

func TestCheckBudgetPresetWarnsWhenUndersized(t *testing.T) {
	path := writeBudgetsYAML(t, 500)
	checks := CheckBudgetPreset(path, "default", 1000)
	if checks[0].Status != StatusWarn {
		t.Errorf("checks[0] = %+v, want warn", checks[0])
	}
}

func TestCheckBudgetPresetErrorsOnUnknownPreset(t *testing.T) {
	path := writeBudgetsYAML(t, 10000)
	checks := CheckBudgetPreset(path, "nonexistent", 1000)
	if checks[0].Status != StatusError {
		t.Errorf("checks[0] = %+v, want error", checks[0])
	}
}
