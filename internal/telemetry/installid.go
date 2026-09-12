package telemetry

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// InstallID returns this machine/user's random per-installation id,
// generating and persisting one at ~/.orch/install_id on first use.
//
// It identifies an orch installation, never a project or a person: no
// project id, path, or hostname is derived from it or stored alongside
// it, and it is the same value across every project this user runs orch
// against on this machine.
func InstallID() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find the home directory: %w", err)
	}
	dir := filepath.Join(home, ".orch")
	path := filepath.Join(dir, "install_id")

	if b, err := os.ReadFile(path); err == nil { // #nosec G304 -- fixed filename under the user's own home directory
		if id := strings.TrimSpace(string(b)); id != "" {
			return id, nil
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("read %s: %w", path, err)
	}

	id, err := randomID()
	if err != nil {
		return "", fmt.Errorf("generate an install id: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	if err := os.WriteFile(path, []byte(id+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return id, nil
}

// randomID is 16 random bytes, hex-encoded — long enough to be unique
// across every orch install without carrying any structure worth reading
// into (it is not a UUID, not derived from the machine, and not a secret).
func randomID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
