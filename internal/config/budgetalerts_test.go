package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// notifications.budget_alerts is on by default (a notifier with no webhook
// sends nothing), and budget_alert_pct must be a share of the cap.
func TestBudgetAlertKeys(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		on      bool
		pct     float64
		wantErr bool
	}{
		{"defaults", "", true, 80, false},
		{"set", "notifications:\n  budget_alerts: false\n  budget_alert_pct: 90\n", false, 90, false},
		{"zero is refused", "notifications:\n  budget_alert_pct: 0\n", false, 0, true},
		{"over 100 is refused", "notifications:\n  budget_alert_pct: 120\n", false, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			write(t, path, c.yaml)
			res, err := Load(path, filepath.Dir(path))
			if c.wantErr {
				if err == nil || !strings.Contains(err.Error(), "notifications.budget_alert_pct") {
					t.Fatalf("Load error = %v, want one naming notifications.budget_alert_pct", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			n := res.Config.Notifications
			if n.BudgetAlerts != c.on || n.BudgetAlertPct != c.pct {
				t.Errorf("budget_alerts=%v budget_alert_pct=%v, want %v %v", n.BudgetAlerts, n.BudgetAlertPct, c.on, c.pct)
			}
			for _, w := range res.Warnings {
				if strings.Contains(w, "budget_alert") {
					t.Errorf("a budget alert key warned as unknown: %s", w)
				}
			}
		})
	}
}
