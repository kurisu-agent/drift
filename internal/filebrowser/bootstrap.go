// Package filebrowser materializes the pinned filebrowser binary on
// the circuit so `circuit.browse` can spawn it without depending on
// whatever (if anything) the operator has on $PATH.
//
// Mirrors internal/devpod/bootstrap: download a release asset, verify
// SHA256 against a baked-in pin, write to <driftHome>/bin/filebrowser.
// Filebrowser ships as a tar.gz (binary + CHANGELOG + LICENSE + README)
// instead of a bare binary, so the verify-and-extract paths look a bit
// different from devpod's; the caching shape is the same.
package filebrowser

import (
	"archive/tar"
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
	"runtime"

	"github.com/kurisu-agent/drift/internal/githttp"
)

// Pin — the filebrowser release lakitu downloads on first run.
//
// Bumping: update pinnedVersion, then for each arch run
//
//	curl -sL https://github.com/filebrowser/filebrowser/releases/download/<ver>/linux-<arch>-filebrowser.tar.gz \
//	    | sha256sum
//
// and paste the value into pinnedTarballHashes.
const (
	pinnedVersion = "v2.63.2"
	pinnedOwner   = "filebrowser"
	pinnedRepo    = "filebrowser"
)

// pinnedTarballHashes is the per-arch SHA256 of the release tar.gz.
// Keyed by "<goos>_<goarch>" (lowercase). Missing keys mean the arch
// isn't built upstream — EnsurePinned surfaces a clear error then.
var pinnedTarballHashes = map[string]string{
	"linux_amd64": "5a6bb687af0a4cf6148a6e09b6fc45f60e8d4b159db37b7138f81fc97033a9bb",
	"linux_arm64": "246938e22a1d44caae43f114eb087a8553f4fa008fb01155e1acd89a80d257f1",
}

// PinnedVersion exposes the pinned release tag for diagnostics.
func PinnedVersion() string { return pinnedVersion }

// EnsurePinned materializes the pinned filebrowser binary at
// <driftHome>/bin/filebrowser and returns its path. On first run it
// downloads the release tarball matching runtime.GOOS/GOARCH,
// SHA256-verifies the tarball against the baked-in pin, and extracts
// the `filebrowser` entry into place; subsequent runs short-circuit
// when the cached destination is present and the sidecar records the
// same pinned tarball hash.
//
// Returns a wrapped error (never partial state) on network failure,
// SHA mismatch, missing archive entry, or unsupported arch.
func EnsurePinned(ctx context.Context, driftHome string) (string, error) {
	if driftHome == "" {
		return "", errors.New("filebrowser: EnsurePinned: driftHome is required")
	}
	goos, goarch := runtime.GOOS, runtime.GOARCH
	key := goos + "_" + goarch
	wantHex, ok := pinnedTarballHashes[key]
	if !ok {
		return "", fmt.Errorf("filebrowser: no pinned binary for %s/%s", goos, goarch)
	}
	dir := filepath.Join(driftHome, "bin")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("filebrowser: mkdir %s: %w", dir, err)
	}
	dest := filepath.Join(dir, "filebrowser")
	sidecar := dest + ".sha256"

	// Cached: dest exists AND sidecar records the matching pinned hash.
	// Hashing the tarball on every call would re-download; trusting the
	// sidecar matches devpod's "compare cached file against pin" shape
	// while still letting an operator force a refresh by removing it.
	if cached, err := os.ReadFile(sidecar); err == nil {
		if string(cached) == wantHex {
			if _, err := os.Stat(dest); err == nil {
				return dest, nil
			}
		}
	}

	url := fmt.Sprintf("https://github.com/%s/%s/releases/download/%s/linux-%s-filebrowser.tar.gz",
		pinnedOwner, pinnedRepo, pinnedVersion, goarch)
	if err := downloadAndExtract(ctx, url, dest, wantHex); err != nil {
		return "", err
	}
	if err := os.WriteFile(sidecar, []byte(wantHex), 0o600); err != nil {
		return "", fmt.Errorf("filebrowser: write sidecar: %w", err)
	}
	return dest, nil
}

// downloadAndExtract streams url into a tempfile, SHA256s it during
// the write (one pass), then untars the `filebrowser` entry into a
// sibling tempfile of dest and renames atomically. Concurrent callers
// write unique tempfiles; last rename wins.
func downloadAndExtract(ctx context.Context, url, dest, wantHex string) error {
	archive, err := os.CreateTemp(filepath.Dir(dest), "filebrowser.tgz.*")
	if err != nil {
		return fmt.Errorf("filebrowser: tempfile: %w", err)
	}
	archivePath := archive.Name()
	defer func() { _ = os.Remove(archivePath) }()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		_ = archive.Close()
		return fmt.Errorf("filebrowser: build request: %w", err)
	}
	resp, err := githttp.DefaultClient().Do(req)
	if err != nil {
		_ = archive.Close()
		return fmt.Errorf("filebrowser: download %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_ = archive.Close()
		return fmt.Errorf("filebrowser: download %s: %s", url, resp.Status)
	}

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(archive, h), resp.Body); err != nil {
		_ = archive.Close()
		return fmt.Errorf("filebrowser: stream body: %w", err)
	}
	if err := archive.Close(); err != nil {
		return fmt.Errorf("filebrowser: close archive: %w", err)
	}
	gotHex := hex.EncodeToString(h.Sum(nil))
	if gotHex != wantHex {
		return fmt.Errorf("filebrowser: SHA256 mismatch from %s: want %s got %s", url, wantHex, gotHex)
	}
	return extractBinary(archivePath, dest)
}

// extractBinary opens the verified tarball and copies the
// `filebrowser` entry into a tempfile next to dest, then renames.
// Other entries (CHANGELOG.md, LICENSE, README.md) are skipped.
func extractBinary(archivePath, dest string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("filebrowser: open archive: %w", err)
	}
	defer func() { _ = f.Close() }()
	gzr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("filebrowser: gzip: %w", err)
	}
	defer func() { _ = gzr.Close() }()
	tr := tar.NewReader(gzr)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("filebrowser: archive missing 'filebrowser' entry")
		}
		if err != nil {
			return fmt.Errorf("filebrowser: tar next: %w", err)
		}
		if hdr.Name != "filebrowser" {
			continue
		}
		return writeBinary(tr, dest)
	}
}

func writeBinary(r io.Reader, dest string) error {
	tmp, err := os.CreateTemp(filepath.Dir(dest), "filebrowser.bin.*")
	if err != nil {
		return fmt.Errorf("filebrowser: tempfile: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := io.Copy(tmp, r); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("filebrowser: extract: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("filebrowser: close tmp: %w", err)
	}
	// #nosec G302 -- the written file is an executable we own and
	// intend to run; 0700 keeps group/world out, 0600 would make it
	// non-executable which defeats the point.
	if err := os.Chmod(tmpPath, 0o700); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("filebrowser: chmod: %w", err)
	}
	if err := os.Rename(tmpPath, dest); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("filebrowser: rename -> %s: %w", dest, err)
	}
	return nil
}
