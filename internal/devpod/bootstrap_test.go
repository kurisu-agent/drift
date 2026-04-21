package devpod

import (
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
	if len(pinnedHashes) == 0 {
		t.Error("pinnedHashes is empty — no arches pinned")
	}
}

func TestDownloadAndVerifyHappyPath(t *testing.T) {
	t.Parallel()
	payload := []byte("pretend this is devpod")
	sum := sha256.Sum256(payload)
	wantHex := hex.EncodeToString(sum[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "devpod")
	if err := downloadAndVerify(context.Background(), srv.URL, dest, wantHex); err != nil {
		t.Fatalf("downloadAndVerify: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if string(got) != string(payload) {
		t.Errorf("payload mismatch")
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Errorf("dest is not executable: %v", info.Mode())
	}
}

func TestDownloadAndVerifyShaMismatchLeavesNoBinary(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("wrong bytes"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "devpod")
	err := downloadAndVerify(context.Background(), srv.URL, dest, "deadbeef")
	if err == nil {
		t.Fatal("want error on SHA mismatch, got nil")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("dest should not exist after mismatch, got err=%v", err)
	}
}

func TestDownloadAndVerifyHTTPErrorLeavesNoBinary(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "devpod")
	err := downloadAndVerify(context.Background(), srv.URL, dest, "aa")
	if err == nil {
		t.Fatal("want error on 404, got nil")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("dest should not exist after 404: err=%v", err)
	}
}

func TestEnsurePinnedRequiresDriftHome(t *testing.T) {
	t.Parallel()
	if _, err := EnsurePinned(context.Background(), ""); err == nil {
		t.Error("want error on empty driftHome, got nil")
	}
}

// The cached-shortcut path of EnsurePinned — "file at dest already has
// the pinned hash, skip the download" — would need an injectable
// constants seam to test deterministically. The branch is a five-line
// hash-compare that's trivially reviewable; cover downloadAndVerify
// above and leave the shortcut.
