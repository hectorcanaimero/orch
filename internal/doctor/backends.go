package doctor

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/hectorcanaimero/orch/internal/model"
	"github.com/hectorcanaimero/orch/internal/router"
)

// probeTimeout caps every CLI probe this file runs — CHECKLIST rule 18.
// Python's own default is 5s (_PROBE_TIMEOUT_S); kept identical so a CLI
// this diagnoses as "hung" under Python is diagnosed the same way here.
const probeTimeout = 5 * time.Second

// knownBackends is probed when tasks.json references no routable model
// (fresh scaffold, empty file) — gemini/agy are deliberately excluded here
// (only probed when a task actually routes to one), matching
// _KNOWN_BACKENDS's own comment: an operator without those optional CLIs
// installed should not get spurious `error` findings on a fresh project.
var knownBackends = []string{"claude", "codex", "opencode"}

// ReferencedBackends returns the backend names tasks actually route to via
// r, sorted; falls back to knownBackends when none resolve. Ports
// _referenced_backends.
func ReferencedBackends(tasks []model.Task, r router.Router) []string {
	used := map[string]bool{}
	for _, t := range tasks {
		if entry, ok := r[t.Model]; ok {
			used[string(entry.Backend)] = true
		}
	}
	if len(used) == 0 {
		out := append([]string(nil), knownBackends...)
		sort.Strings(out)
		return out
	}
	out := make([]string, 0, len(used))
	for b := range used {
		out = append(out, b)
	}
	sort.Strings(out)
	return out
}

// CheckBackends probes presence, version, and a cheap auth signal for each
// name in backends. Ports check_backends, minus its ThreadPoolExecutor
// concurrency (Go's test suite and CLI both tolerate probing sequentially;
// with a 5s cap per probe and normally 3-5 backends, the worst case is
// still well under what an interactive `orch doctor` run should feel like
// — parallelizing is a straightforward follow-up, not a behavior change,
// if that ever proves wrong in practice).
func CheckBackends(backends []string) []Check {
	var out []Check
	for _, name := range backends {
		path, err := exec.LookPath(name)
		if err != nil {
			out = append(out, Check{
				Name:        "backend." + name,
				Status:      StatusError,
				Detail:      name + " not on PATH",
				Remediation: "Install " + name + " and ensure it's on your $PATH.",
			})
			out = append(out, authCheck(name, false))
			continue
		}
		ok, detail := probeVersion(name)
		if ok {
			out = append(out, Check{
				Name:   "backend." + name,
				Status: StatusOK,
				Detail: detail + " at " + path,
			})
		} else {
			out = append(out, Check{
				Name:        "backend." + name,
				Status:      StatusError,
				Detail:      detail,
				Remediation: "Verify " + name + " is installed correctly and on PATH.",
			})
		}
		out = append(out, authCheck(name, true))
	}
	return out
}

// probeVersion runs `<cli> --version` and reports (ok, first line of
// output). Ports _probe_version.
func probeVersion(cli string) (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	// #nosec G204 -- cli is one of a fixed, small set of known backend names.
	cmd := exec.CommandContext(ctx, cli, "--version")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	text := firstLine(stdout.String())
	if text == "" {
		text = firstLine(stderr.String())
	}
	if text == "" {
		text = "(no output)"
	}
	return err == nil, text
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(line)
}

// authCheck is the cheap, read-only auth signal for one backend. Ports the
// five _probe_*_auth functions: opencode is the only one with a real
// non-interactive probe (`opencode auth list`); claude/codex have none and
// report `skip`; gemini/agy report `ok` when installed (presence is the
// best cheap signal) and `skip` when not. installed lets the caller skip
// the (possibly real) probe work when CheckBackends already knows the
// binary is missing.
func authCheck(name string, installed bool) Check {
	base := "backend." + name + ".auth"
	if !installed {
		return Check{Name: base, Status: StatusSkip, Detail: name + " CLI not installed — auth probe skipped"}
	}
	switch name {
	case "opencode":
		return probeOpencodeAuth()
	case "claude", "codex":
		return Check{Name: base, Status: StatusSkip,
			Detail: "no cheap auth probe for " + name + " CLI — assumed ok (run `" + name + "` interactively to verify)"}
	default: // gemini, agy, and any future backend with no dedicated probe
		return Check{Name: base, Status: StatusOK, Detail: name + " auth assumed ok (CLI present)"}
	}
}

func probeOpencodeAuth() Check {
	const name = "backend.opencode.auth"
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "opencode", "auth", "list")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if ctx.Err() != nil {
		return Check{Name: name, Status: StatusWarn, Detail: "opencode auth list timed out (> 5s)"}
	}
	var exitErr *exec.ExitError
	if err != nil {
		if !errors.As(err, &exitErr) {
			return Check{Name: name, Status: StatusWarn, Detail: "opencode auth list failed: " + err.Error()}
		}
		detail := firstLine(stderr.String())
		if detail == "" {
			detail = firstLine(stdout.String())
		}
		if detail == "" {
			detail = "auth list nonzero exit"
		}
		if len(detail) > 200 {
			detail = detail[:200]
		}
		return Check{Name: name, Status: StatusError, Detail: detail, Remediation: "Run `opencode auth login` to authenticate."}
	}
	return Check{Name: name, Status: StatusOK, Detail: "opencode auth list ok"}
}

func isExitError(err error, target **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError)
	if ok {
		*target = e
	}
	return ok
}
