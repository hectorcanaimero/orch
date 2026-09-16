package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// dashboard.language is the key; summary_language is its older spelling and
// still works. A language orch cannot write is refused at load instead of
// quietly becoming Spanish.
func TestDashboardLanguage(t *testing.T) {
	cases := []struct {
		name, yaml, want, wantErr string
	}{
		{"default stays Spanish", "", "es", ""},
		{"language", "dashboard:\n  language: pt\n", "pt", ""},
		{"old key alone", "dashboard:\n  summary_language: en\n", "en", ""},
		{"language wins over the old key", "dashboard:\n  summary_language: es\n  language: en\n", "en", ""},
		{"case and region are tolerated", "dashboard:\n  language: PT-BR\n", "pt", ""},
		{"unknown language", "dashboard:\n  language: fr\n", "", "dashboard.language"},
		{"unknown language in the old key", "dashboard:\n  summary_language: de\n", "", "dashboard.summary_language"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			write(t, path, c.yaml)
			res, err := Load(path, filepath.Dir(path))
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) || !strings.Contains(err.Error(), "en, es, pt") {
					t.Fatalf("Load error = %v, want one naming %s and the choices", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := res.Config.Dashboard.SummaryLanguage; got != c.want {
				t.Errorf("SummaryLanguage = %q, want %q", got, c.want)
			}
			if got := res.Config.Dashboard.Language; got != c.want {
				t.Errorf("Language = %q, want %q", got, c.want)
			}
			for _, w := range res.Warnings {
				if strings.Contains(w, "dashboard.language") {
					t.Errorf("dashboard.language warned as unknown: %s", w)
				}
			}
		})
	}
}
