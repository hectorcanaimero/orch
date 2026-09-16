package doctor

import (
	"strings"
	"testing"

	"github.com/hectorcanaimero/orch/internal/tunnel"
)

func TestCheckTunnel(t *testing.T) {
	found := tunnel.Binary{Found: true, Path: "/usr/bin/cloudflared", Version: "cloudflared version 2026.9.1"}
	for _, tc := range []struct {
		name    string
		enabled bool
		bin     tunnel.Binary
		blocker string
		want    Status
		remedy  string
	}{
		{"disabled skips", false, tunnel.Binary{}, "", StatusSkip, ""},
		{"missing binary warns with the install steps", true, tunnel.Binary{}, "", StatusWarn, "cloudflared --version"},
		{"a config file blocks quick tunnels", true, found, "/home/u/.cloudflared/config.yml", StatusWarn, "mv /home/u/.cloudflared/config.yml"},
		{"ready", true, found, "", StatusOK, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := CheckTunnel(tc.enabled, tc.bin, tc.blocker)
			if got.Status != tc.want || !strings.Contains(got.Remediation, tc.remedy) {
				t.Errorf("got %+v, want %s with remediation containing %q", got, tc.want, tc.remedy)
			}
		})
	}
}
