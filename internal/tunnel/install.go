package tunnel

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// This file is the one source of "how do I get cloudflared": the dashboard's
// Tunnel page reads it from /api/tunnel/capabilities, `orch dashboard
// --tunnel` prints it when the binary is missing, and `orch doctor` points
// at it. Commands are Cloudflare's own (pkg.cloudflare.com and the
// cloudflared GitHub releases, checked 2026-09-16). orch never runs them:
// installing a package needs sudo and trust decisions that belong to the
// person at the keyboard.

// DocsURL is Cloudflare's download page, for any system the guides miss.
const DocsURL = "https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/downloads/"

// Guide ids, stable on the wire.
const (
	GuideBrew        = "brew"
	GuideApt         = "apt"
	GuideDnf         = "dnf"
	GuideLinuxBinary = "linux-binary"
	GuideMacBinary   = "macos-binary"
)

// InstallStep is one command to copy and run.
type InstallStep struct {
	Title   string `json:"title"`
	Command string `json:"command"`
}

// InstallGuide is one way to put cloudflared on PATH.
type InstallGuide struct {
	ID    string        `json:"id"`
	Label string        `json:"label"`
	Steps []InstallStep `json:"steps"`
}

var verifyStep = InstallStep{Title: "Check it is on PATH", Command: "cloudflared --version"}

// InstallGuides lists every guide, the binary ones for goarch (runtime.GOARCH
// naming). Order is the order a page lists them in.
func InstallGuides(goarch string) []InstallGuide {
	const release = "https://github.com/cloudflare/cloudflared/releases/latest/download/"
	linuxArch := goarch
	if linuxArch == "" {
		linuxArch = "amd64"
	}
	macArch := "arm64"
	if goarch == "amd64" {
		macArch = "amd64"
	}
	return []InstallGuide{
		{ID: GuideBrew, Label: "macOS (Homebrew)", Steps: []InstallStep{
			{Title: "Install", Command: "brew install cloudflared"},
			verifyStep,
		}},
		{ID: GuideApt, Label: "Debian / Ubuntu (apt)", Steps: []InstallStep{
			{Title: "Add Cloudflare's signing key", Command: "sudo mkdir -p --mode=0755 /usr/share/keyrings\n" +
				"curl -fsSL https://pkg.cloudflare.com/cloudflare-main.gpg | sudo tee /usr/share/keyrings/cloudflare-main.gpg >/dev/null"},
			{Title: "Add the repository", Command: "echo 'deb [signed-by=/usr/share/keyrings/cloudflare-main.gpg] " +
				"https://pkg.cloudflare.com/cloudflared any main' | sudo tee /etc/apt/sources.list.d/cloudflared.list"},
			{Title: "Install", Command: "sudo apt-get update && sudo apt-get install cloudflared"},
			verifyStep,
		}},
		{ID: GuideDnf, Label: "Fedora / RHEL 8+ (dnf)", Steps: []InstallStep{
			{Title: "Add the repository", Command: "sudo dnf config-manager --add-repo https://pkg.cloudflare.com/cloudflared.repo"},
			{Title: "Install", Command: "sudo dnf install cloudflared"},
			verifyStep,
		}},
		{ID: GuideLinuxBinary, Label: "Other Linux (single binary)", Steps: []InstallStep{
			{Title: "Download it into ~/.local/bin", Command: "mkdir -p ~/.local/bin\n" +
				"curl -fL -o ~/.local/bin/cloudflared " + release + "cloudflared-linux-" + linuxArch},
			{Title: "Make it executable", Command: "chmod +x ~/.local/bin/cloudflared"},
			{Title: "Check it is on PATH (add ~/.local/bin to PATH if not)", Command: verifyStep.Command},
		}},
		{ID: GuideMacBinary, Label: "macOS without Homebrew", Steps: []InstallStep{
			{Title: "Download and unpack it into ~/.local/bin", Command: "mkdir -p ~/.local/bin\n" +
				"curl -fL " + release + "cloudflared-darwin-" + macArch + ".tgz | tar -xz -C ~/.local/bin"},
			{Title: "Check it is on PATH (add ~/.local/bin to PATH if not)", Command: verifyStep.Command},
		}},
	}
}

// RecommendedGuide picks the guide for a machine: goos is runtime.GOOS,
// osRelease the content of /etc/os-release ("" when absent), hasBrew whether
// brew is on PATH. "" means no guide fits and DocsURL is the answer.
func RecommendedGuide(goos, osRelease string, hasBrew bool) string {
	switch goos {
	case "darwin":
		if hasBrew {
			return GuideBrew
		}
		return GuideMacBinary
	case "linux":
		for _, id := range osReleaseIDs(osRelease) {
			switch id {
			case "debian", "ubuntu":
				return GuideApt
			case "fedora", "rhel", "centos", "rocky", "almalinux":
				return GuideDnf
			}
		}
		return GuideLinuxBinary
	}
	return ""
}

// osReleaseIDs is ID followed by every word of ID_LIKE, lowercased and
// unquoted — "linuxmint" is only recognisable through its ID_LIKE=ubuntu.
func osReleaseIDs(content string) []string {
	var ids []string
	for _, line := range strings.Split(content, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || (key != "ID" && key != "ID_LIKE") {
			continue
		}
		value = strings.ToLower(strings.Trim(value, `"'`))
		ids = append(ids, strings.Fields(value)...)
	}
	return ids
}

// Host describes this machine for the install guide.
type Host struct {
	OS               string `json:"os"`
	Arch             string `json:"arch"`
	RecommendedGuide string `json:"recommended_guide"`
}

// DetectHost reads runtime, /etc/os-release and PATH.
func DetectHost() Host {
	osRelease, _ := os.ReadFile("/etc/os-release")
	_, brewErr := exec.LookPath("brew")
	return Host{
		OS:               runtime.GOOS,
		Arch:             runtime.GOARCH,
		RecommendedGuide: RecommendedGuide(runtime.GOOS, string(osRelease), brewErr == nil),
	}
}

// Binary is what PATH says about cloudflared.
type Binary struct {
	Found   bool   `json:"found"`
	Path    string `json:"path,omitempty"`
	Version string `json:"version,omitempty"`
}

// LookupBinary finds command (cloudflared when empty) and asks its version,
// bounded so a wedged binary cannot hang a page load.
func LookupBinary(command string) Binary {
	if command == "" {
		command = Command
	}
	path, err := exec.LookPath(command)
	if err != nil {
		return Binary{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, path, "--version").Output() // #nosec G204 -- fixed binary found on PATH, fixed flag
	version, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return Binary{Found: true, Path: path, Version: version}
}

// ConfigBlocker returns the first cloudflared config file that would stop a
// quick tunnel, or "". Cloudflare: "TryCloudflare quick tunnels are
// currently not supported if a config.yaml configuration file is present in
// the .cloudflared directory". cloudflared searches these directories, so a
// file in any of them has the same effect.
func ConfigBlocker() string {
	home, _ := os.UserHomeDir()
	dirs := []string{"/etc/cloudflared", "/usr/local/etc/cloudflared"}
	if home != "" {
		dirs = append([]string{
			filepath.Join(home, ".cloudflared"),
			filepath.Join(home, ".cloudflare-warp"),
			filepath.Join(home, "cloudflare-warp"),
		}, dirs...)
	}
	return findConfig(dirs)
}

func findConfig(dirs []string) string {
	for _, dir := range dirs {
		for _, name := range []string{"config.yml", "config.yaml"} {
			path := filepath.Join(dir, name)
			if _, err := os.Stat(path); err == nil {
				return path
			}
		}
	}
	return ""
}

// MissingBinaryMessage is what the CLI and doctor print when cloudflared is
// not on PATH: why it is needed and the steps for this machine.
func MissingBinaryMessage() string {
	var b strings.Builder
	b.WriteString("cloudflared is not on PATH. The dashboard tunnel is a Cloudflare quick tunnel " +
		"and needs it (no Cloudflare account required).\n")
	host := DetectHost()
	for _, g := range InstallGuides(host.Arch) {
		if g.ID != host.RecommendedGuide {
			continue
		}
		fmt.Fprintf(&b, "\nInstall it — %s:\n", g.Label)
		for i, step := range g.Steps {
			fmt.Fprintf(&b, "  %d. %s\n", i+1, step.Title)
			for _, line := range strings.Split(step.Command, "\n") {
				fmt.Fprintf(&b, "     $ %s\n", line)
			}
		}
	}
	fmt.Fprintf(&b, "\nOther systems: %s\n", DocsURL)
	return b.String()
}

// BlockerMessage explains a ConfigBlocker hit.
func BlockerMessage(path string) string {
	return fmt.Sprintf("%s exists, and Cloudflare quick tunnels do not start while a cloudflared "+
		"config file is present. Rename it while you use the dashboard tunnel "+
		"(for example: mv %s %s.bak).", path, path, path)
}
