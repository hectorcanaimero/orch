package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// archiveWith builds a goreleaser-shaped archive: orch plus the extra files.
func archiveWith(t *testing.T, binary []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, data := range map[string][]byte{"LICENSE": []byte("MIT"), "orch": binary} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func releaseServer(t *testing.T, archive []byte, sum string) *httptest.Server {
	t.Helper()
	asset := AssetName("v0.13.1", "linux", "amd64")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v0.13.1/checksums.txt":
			_, _ = w.Write([]byte(strings.Repeat("0", 64) + "  orch_v0.13.1_darwin_arm64.tar.gz\n" + sum + "  " + asset + "\n"))
		case "/v0.13.1/" + asset:
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestBinaryVerifiesTheChecksum(t *testing.T) {
	archive := archiveWith(t, []byte("#!new orch"))
	sum := sha256.Sum256(archive)
	srv := releaseServer(t, archive, hex.EncodeToString(sum[:]))

	got, err := Downloader{Base: srv.URL}.Binary(context.Background(), "v0.13.1", "linux", "amd64")
	if err != nil {
		t.Fatalf("Binary: %v", err)
	}
	if string(got) != "#!new orch" {
		t.Errorf("binary = %q", got)
	}
}

func TestBinaryRefusesAMismatchedDownload(t *testing.T) {
	archive := archiveWith(t, []byte("#!tampered"))
	srv := releaseServer(t, archive, strings.Repeat("a", 64))

	_, err := Downloader{Base: srv.URL}.Binary(context.Background(), "v0.13.1", "linux", "amd64")
	if err == nil || !strings.Contains(err.Error(), "does not match its checksum") {
		t.Errorf("err = %v, want a checksum refusal", err)
	}
}

func TestReplaceSwapsTheBinaryInPlace(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "orch")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil { // #nosec G306 -- a test executable
		t.Fatal(err)
	}
	if err := Replace(target, []byte("new")); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	got, err := os.ReadFile(target) // #nosec G304 -- a temp dir this test made
	if err != nil || string(got) != "new" {
		t.Fatalf("target = %q, %v", got, err)
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Errorf("the new orch is not executable: %v %v", info.Mode(), err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("left %d files behind, want only orch", len(entries))
	}
}

func TestManagedBy(t *testing.T) {
	if ManagedBy("/opt/homebrew/Cellar/orch/0.13.0/bin/orch") == "" {
		t.Errorf("a Homebrew binary was treated as self-managed")
	}
	if ManagedBy("/usr/local/bin/orch") != "" {
		t.Errorf("an install.sh binary was treated as managed")
	}
}
