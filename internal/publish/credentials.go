package publish

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Env overrides for CI. A CI job that publishes one project needs only that
// project's publish token and the Worker URL — never the admin token, and
// never a credentials file on the runner.
const (
	EnvCloudURL          = "ORCH_CLOUD_URL"
	EnvCloudPublishToken = "ORCH_CLOUD_PUBLISH_TOKEN" // #nosec G101 -- the NAME of the env var, not a credential
)

// CredentialsFile is the operator's ~/.orch/credentials: secrets that belong
// to a person on a machine, not to a project, which is why they are not in
// the project's config.yaml (committed) or its database (copied around with
// the project).
//
// The top level is kept as raw JSON so a block this binary does not know —
// written by a newer orch, or by hand — survives a save.
type CredentialsFile struct {
	Cloud *CloudCredentials
	other map[string]json.RawMessage
}

// CloudCredentials is the `cloud` block.
type CloudCredentials struct {
	URL        string                  `json:"url"`
	AdminToken string                  `json:"admin_token,omitempty"`
	Projects   map[string]CloudProject `json:"projects,omitempty"`
}

// CloudProject holds one project's tokens on this machine.
type CloudProject struct {
	PublishToken string `json:"publish_token,omitempty"`
	ViewToken    string `json:"view_token,omitempty"`
}

// DefaultCredentialsPath is ~/.orch/credentials.
func DefaultCredentialsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find the home directory for ~/.orch/credentials: %w", err)
	}
	return filepath.Join(home, ".orch", "credentials"), nil
}

// LoadCredentials reads path. A missing file is an empty CredentialsFile,
// not an error: that is every machine before its first `orch cloud login`.
func LoadCredentials(path string) (CredentialsFile, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- the operator's own credentials path
	if errors.Is(err, fs.ErrNotExist) {
		return CredentialsFile{}, nil
	}
	if err != nil {
		return CredentialsFile{}, fmt.Errorf("read %s: %w", path, err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return CredentialsFile{}, fmt.Errorf("%s is not valid JSON: %w", path, err)
	}
	cf := CredentialsFile{other: top}
	if raw, ok := top["cloud"]; ok {
		var cc CloudCredentials
		if err := json.Unmarshal(raw, &cc); err != nil {
			return CredentialsFile{}, fmt.Errorf("%s: the cloud block is not valid: %w", path, err)
		}
		cf.Cloud = &cc
		delete(cf.other, "cloud")
	}
	return cf, nil
}

// SaveCredentials writes cf to path atomically with mode 0600, creating the
// directory with 0700 if it does not exist.
//
// Atomic because the file holds tokens the Worker will never show again: a
// write interrupted halfway must leave the previous file, not a truncated
// one and a project nobody can publish to. The temporary file is created in
// the same directory, so the rename cannot cross a filesystem.
func SaveCredentials(path string, cf CredentialsFile) error {
	top := make(map[string]json.RawMessage, len(cf.other)+1)
	for k, v := range cf.other {
		top[k] = v
	}
	if cf.Cloud != nil {
		raw, err := json.Marshal(cf.Cloud)
		if err != nil {
			return fmt.Errorf("encoding the cloud credentials: %w", err)
		}
		top["cloud"] = raw
	} else {
		delete(top, "cloud")
	}
	data, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding %s: %w", path, err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".credentials-*")
	if err != nil {
		return fmt.Errorf("create a temporary file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	// CreateTemp already opens with 0600; the Chmod makes that a property
	// of this function rather than of the standard library's default.
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("chmod %s: %w", tmpName, err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	committed = true
	return nil
}

// CloudTarget is everything one publish needs, resolved from the
// environment or the credentials file.
type CloudTarget struct {
	URL          string
	AdminToken   string
	PublishToken string
	ViewToken    string
	// FromEnv is true when ORCH_CLOUD_URL and ORCH_CLOUD_PUBLISH_TOKEN
	// supplied the target: nothing is read from or written to the
	// credentials file, and the viewer URL is unknown (the view token lives
	// with the operator, not with CI).
	FromEnv bool
}

// ResolveCloudTarget picks the target for project id. getenv is os.Getenv
// outside tests.
//
// Env wins only when it is complete: a URL without a token (or the reverse)
// is refused by name rather than half-merged with the credentials file,
// because a CI job silently publishing with the operator's stored token is
// exactly what the env override exists to avoid.
func ResolveCloudTarget(cf CredentialsFile, id string, getenv func(string) string) (CloudTarget, error) {
	envURL := strings.TrimSpace(getenv(EnvCloudURL))
	envTok := strings.TrimSpace(getenv(EnvCloudPublishToken))
	switch {
	case envURL != "" && envTok != "":
		return CloudTarget{URL: envURL, PublishToken: envTok, FromEnv: true}, nil
	case envURL != "" || envTok != "":
		return CloudTarget{}, fmt.Errorf("%s and %s go together — set both to publish from CI, or neither "+
			"to use ~/.orch/credentials", EnvCloudURL, EnvCloudPublishToken)
	}
	if cf.Cloud == nil || cf.Cloud.URL == "" {
		return CloudTarget{}, errors.New("no orch-cloud Worker configured — run " +
			"`orch cloud login --url <worker URL>` with the Worker's ADMIN_TOKEN on stdin " +
			"(see docs/CLOUD.md)")
	}
	t := CloudTarget{URL: cf.Cloud.URL, AdminToken: cf.Cloud.AdminToken}
	if p, ok := cf.Cloud.Projects[id]; ok {
		t.PublishToken, t.ViewToken = p.PublishToken, p.ViewToken
	}
	return t, nil
}
