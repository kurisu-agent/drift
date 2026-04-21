package devpod

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
)

// Pin — the devpod release lakitu downloads on first run.
//
// Bumping: update pinnedVersion, then for each arch run
//
//	gh release download <ver> --repo <owner>/<repo> \
//	    --pattern "devpod-<goos>-<goarch>"
//	sha256sum devpod-<goos>-<goarch>
//
// and paste the value into pinnedHashes. The flake has its own
// source-tarball hashes (flake.nix:devpodPin); bumping should flow
// through both.
const (
	pinnedVersion = "v0.22.0"
	pinnedOwner   = "skevetter"
	pinnedRepo    = "devpod"
)

// pinnedHashes is the per-arch SHA256 of the release binary. Keyed by
// "<goos>_<goarch>" (lowercase). Missing keys mean the arch isn't
// built upstream — EnsurePinned surfaces a clear error in that case.
var pinnedHashes = map[string]string{
	"linux_amd64": "6365f2af903d4778a5acd7d2b6577f0a90f29701577e540c22922fb53dcd5cbc",
	"linux_arm64": "6e17dcd08583ad2acd85d246522c5cf0868236560cdeae2e32991277795ba19f",
}

// PinnedVersion exposes the pinned release tag for diagnostics (e.g.
// `lakitu init` renders it so the operator can confirm what they're
// running before the download happens).
func PinnedVersion() string { return pinnedVersion }

// EnsurePinned materializes the pinned devpod binary at
// <driftHome>/bin/devpod and returns its path. On first run it
// downloads the release asset matching runtime.GOOS/GOARCH from the
// upstream fork and SHA256-verifies against the baked-in pin;
// subsequent runs short-circuit on a hash match. Atomic via
// tmpfile-then-rename so concurrent starts don't corrupt each other.
//
// Returns a wrapped error (never partial state) on network failure,
// SHA mismatch, or unsupported arch. Callers decide whether to fall
// back to $PATH or fail loud.
func EnsurePinned(ctx context.Context, driftHome string) (string, error) {
	if driftHome == "" {
		return "", errors.New("devpod: EnsurePinned: driftHome is required")
	}
	goos, goarch := runtime.GOOS, runtime.GOARCH
	key := goos + "_" + goarch
	wantHex, ok := pinnedHashes[key]
	if !ok {
		return "", fmt.Errorf("devpod: no pinned binary for %s/%s", goos, goarch)
	}
	dir := filepath.Join(driftHome, "bin")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("devpod: mkdir %s: %w", dir, err)
	}
	dest := filepath.Join(dir, "devpod")

	// Happy path: already cached with the matching hash.
	if existing, err := os.ReadFile(dest); err == nil {
		got := sha256.Sum256(existing)
		if hex.EncodeToString(got[:]) == wantHex {
			return dest, nil
		}
	}

	url := fmt.Sprintf("https://github.com/%s/%s/releases/download/%s/devpod-%s-%s",
		pinnedOwner, pinnedRepo, pinnedVersion, goos, goarch)
	if err := downloadAndVerify(ctx, url, dest, wantHex); err != nil {
		return "", err
	}
	return dest, nil
}

// downloadAndVerify streams url to a tempfile next to dest, checksums
// during the write (one pass, no re-read), renames on match. Concurrent
// callers write unique tempfiles; last rename wins and is atomic.
func downloadAndVerify(ctx context.Context, url, dest, wantHex string) error {
	tmp, err := os.CreateTemp(filepath.Dir(dest), "devpod.dl.*")
	if err != nil {
		return fmt.Errorf("devpod: tempfile: %w", err)
	}
	tmpPath := tmp.Name()
	// Cleanup on any error path — rename succeeds only after all checks pass.
	cleanup := func() { _ = tmp.Close(); _ = os.Remove(tmpPath) }

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		cleanup()
		return fmt.Errorf("devpod: build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cleanup()
		return fmt.Errorf("devpod: download %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		cleanup()
		return fmt.Errorf("devpod: download %s: %s", url, resp.Status)
	}

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), resp.Body); err != nil {
		cleanup()
		return fmt.Errorf("devpod: stream body: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("devpod: close tmp: %w", err)
	}
	gotHex := hex.EncodeToString(h.Sum(nil))
	if gotHex != wantHex {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("devpod: SHA256 mismatch from %s: want %s got %s", url, wantHex, gotHex)
	}
	// #nosec G302 -- the written file is an executable we own and
	// intend to run; 0700 keeps group/world out, 0600 would make it
	// non-executable which defeats the point.
	if err := os.Chmod(tmpPath, 0o700); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("devpod: chmod: %w", err)
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("devpod: rename -> %s: %w", dest, err)
	}
	return nil
}
