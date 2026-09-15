package update

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Downloader fetches release archives.
type Downloader struct {
	HTTP *http.Client
	// Base is where release files are served from; empty means GitHub.
	Base string
}

const maxArchive = 64 << 20

// Binary downloads tag's archive for goos/goarch, checks it against the
// release's checksums.txt, and returns the orch binary inside it. Nothing is
// trusted without its checksum: a truncated or tampered download never
// reaches the disk.
func (d Downloader) Binary(ctx context.Context, tag, goos, goarch string) ([]byte, error) {
	base := d.Base
	if base == "" {
		base = "https://github.com/" + Repo + "/releases/download"
	}
	asset := AssetName(tag, goos, goarch)
	sums, err := d.get(ctx, base+"/"+tag+"/checksums.txt")
	if err != nil {
		return nil, err
	}
	want, err := checksumFor(sums, asset)
	if err != nil {
		return nil, err
	}
	archive, err := d.get(ctx, base+"/"+tag+"/"+asset)
	if err != nil {
		return nil, err
	}
	got := sha256.Sum256(archive)
	if hex.EncodeToString(got[:]) != want {
		return nil, fmt.Errorf("%s does not match its checksum; not installing it", asset)
	}
	return binaryFrom(archive)
}

func (d Downloader) get(ctx context.Context, url string) ([]byte, error) {
	httpc := d.HTTP
	if httpc == nil {
		httpc = &http.Client{Timeout: 2 * time.Minute}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("building the request for %s: %w", url, err)
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading %s: HTTP %d", url, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxArchive+1))
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", url, err)
	}
	if len(data) > maxArchive {
		return nil, fmt.Errorf("downloading %s: larger than %d MB", url, maxArchive>>20)
	}
	return data, nil
}

// checksumFor finds asset's sha256 in goreleaser's checksums.txt.
func checksumFor(sums []byte, asset string) (string, error) {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && fields[1] == asset {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("checksums.txt has no entry for %s", asset)
}

// binaryFrom returns the file named orch from a .tar.gz.
func binaryFrom(archive []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("opening the release archive: %w", err)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("the release archive has no orch binary")
		}
		if err != nil {
			return nil, fmt.Errorf("reading the release archive: %w", err)
		}
		if h.Typeflag == tar.TypeReg && filepath.Base(h.Name) == "orch" {
			data, err := io.ReadAll(io.LimitReader(tr, maxArchive))
			if err != nil {
				return nil, fmt.Errorf("reading orch from the release archive: %w", err)
			}
			return data, nil
		}
	}
}

// Replace swaps the binary at target for binary: written next to it first,
// then renamed over it, so an interrupted upgrade never leaves half a binary
// where orch was. A running process keeps the old file it already opened.
func Replace(target string, binary []byte) error {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".orch-upgrade-*")
	if err != nil {
		return fmt.Errorf("writing next to %s: %w", target, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(binary); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing the new orch: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing the new orch: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil { // #nosec G302 -- an executable
		return fmt.Errorf("making the new orch executable: %w", err)
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		return fmt.Errorf("replacing %s: %w", target, err)
	}
	return nil
}

// ManagedBy names the package manager that owns the binary at path, or ""
// when orch was installed by hand or by install.sh. A managed binary is
// upgraded through its manager; replacing it underneath would desync it.
func ManagedBy(path string) string {
	if strings.Contains(path, "/Cellar/") || strings.Contains(path, "/homebrew/") || strings.Contains(path, "/linuxbrew/") {
		return "brew upgrade orch"
	}
	return ""
}
