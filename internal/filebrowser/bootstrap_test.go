package filebrowser

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

func TestPinnedVersionLooksSane(t *testing.T) {
	t.Parallel()
	v := PinnedVersion()
	if !strings.HasPrefix(v, "v") {
		t.Errorf("PinnedVersion = %q, expected v<n>.<n>.<n>", v)
	}
	if len(pinnedTarballHashes) == 0 {
		t.Error("pinnedTarballHashes is empty — no arches pinned")
	}
}

// makeTarball returns a gzipped tarball whose `filebrowser` entry has
// the given body, plus arbitrary noise entries that EnsurePinned
// should skip without complaint.
func makeTarball(t *testing.T, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	write := func(name string, data []byte) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(data))}); err != nil {
			t.Fatalf("tar header %s: %v", name, err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatalf("tar write %s: %v", name, err)
		}
	}
	write("README.md", []byte("noise"))
	write("filebrowser", body)
	write("LICENSE", []byte("more noise"))
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gz close: %v", err)
	}
	return buf.Bytes()
}

func TestDownloadAndExtractHappyPath(t *testing.T) {
	t.Parallel()
	body := []byte("pretend this is filebrowser")
	tarball := makeTarball(t, body)
	sum := sha256.Sum256(tarball)
	wantHex := hex.EncodeToString(sum[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(tarball)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "filebrowser")
	if err := downloadAndExtract(context.Background(), srv.URL, dest, wantHex); err != nil {
		t.Fatalf("downloadAndExtract: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("payload mismatch: got %q want %q", got, body)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Errorf("dest is not executable: %v", info.Mode())
	}
}

func TestDownloadAndExtractShaMismatchLeavesNoBinary(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(makeTarball(t, []byte("x")))
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "filebrowser")
	err := downloadAndExtract(context.Background(), srv.URL, dest, "deadbeef")
	if err == nil {
		t.Fatal("want error on SHA mismatch, got nil")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("dest should not exist after mismatch, got err=%v", err)
	}
}

func TestDownloadAndExtractHTTPErrorLeavesNoBinary(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "filebrowser")
	err := downloadAndExtract(context.Background(), srv.URL, dest, "aa")
	if err == nil {
		t.Fatal("want error on 404, got nil")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("dest should not exist after 404: err=%v", err)
	}
}

func TestDownloadAndExtractMissingEntry(t *testing.T) {
	t.Parallel()
	// Tarball without a `filebrowser` entry — the only useful failure
	// mode the parsed-archive path has, and one EnsurePinned must surface
	// loudly so a future upstream rename doesn't silently break the build.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "README.md", Mode: 0o644, Size: 4})
	_, _ = tw.Write([]byte("noop"))
	_ = tw.Close()
	_ = gz.Close()
	tarball := buf.Bytes()
	sum := sha256.Sum256(tarball)
	wantHex := hex.EncodeToString(sum[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(tarball)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "filebrowser")
	err := downloadAndExtract(context.Background(), srv.URL, dest, wantHex)
	if err == nil {
		t.Fatal("want error on missing filebrowser entry, got nil")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Errorf("error should mention missing entry: %v", err)
	}
}

func TestEnsurePinnedRequiresDriftHome(t *testing.T) {
	t.Parallel()
	if _, err := EnsurePinned(context.Background(), ""); err == nil {
		t.Error("want error on empty driftHome, got nil")
	}
}
