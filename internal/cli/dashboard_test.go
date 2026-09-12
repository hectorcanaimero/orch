package cli

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/hectorcanaimero/orch/internal/config"
	"github.com/hectorcanaimero/orch/internal/dashboard"
)

// A listener that never opened must not be announced.
//
// `Server.Ready()` closes on a FAILED listen as well as on a successful one —
// a waiter that blocked forever on a server that is never coming up would be
// worse — so the banner goroutine runs either way. What tells the two apart is
// the bound address, which exists only once the listener does. Before the
// guard this printed `Orch dashboard running on http://` with nothing after
// the slashes, and then the real error underneath it.
//
// Driven through `Serve` against an unresolvable host rather than a busy port:
// it fails on every machine, deterministically, and binds nothing. And asserted
// on `printDashboardBanner` directly rather than through the CLI, because the
// goroutine racing `Serve`'s return makes the end-to-end version pass with the
// guard removed — a test that cannot fail is not covering this.
func TestBannerIsSilentWhenTheListenerNeverOpened(t *testing.T) {
	cfg := dashboard.Config{
		Profile: dashboard.ProfileOperator,
		Host:    "256.256.256.256",
		Port:    7420,
	}
	server, err := dashboard.New(cfg, dashboard.Options{
		Paths:  config.Paths{Root: t.TempDir(), ID: "demo"},
		Static: http.NotFoundHandler(),
	})
	if err != nil {
		t.Fatalf("dashboard.New: %v", err)
	}

	if serveErr := server.Serve(context.Background()); serveErr == nil {
		t.Fatal("Serve succeeded against an unresolvable host")
	}
	select {
	case <-server.Ready():
	default:
		t.Fatal("Ready did not close after a failed listen — a waiter would hang")
	}
	if addr := server.BoundAddr(); addr != "" {
		t.Fatalf("BoundAddr = %q after a failed listen, want empty", addr)
	}

	var out bytes.Buffer
	cmd := &cobra.Command{Use: "dashboard"}
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	printDashboardBanner(cmd, server, cfg, config.Paths{Root: t.TempDir(), ID: "demo"})

	if got := out.String(); strings.Contains(got, "running on") {
		t.Errorf("the banner announced a server that never bound:\n%s", got)
	}
	// Silent, not "partly printed": the reason is already on its way to the
	// caller as Serve's error, and saying it twice in two shapes is worse
	// than saying it once.
	if got := strings.TrimSpace(out.String()); got != "" {
		t.Errorf("the banner printed %q; Serve's error is the one report", got)
	}
}
