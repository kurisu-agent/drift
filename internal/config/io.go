package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// loadYAMLStrict decodes the file at path into dst with KnownFields enabled —
// any YAML key not present in the Go struct produces an error. Returns
// (false, nil) when the file does not exist so callers can distinguish a
// missing config from a malformed one.
func loadYAMLStrict(path string, dst any) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("config: open %s: %w", path, err)
	}
	defer f.Close()

	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(dst); err != nil {
		return false, fmt.Errorf("config: decode %s: %w", path, err)
	}
	return true, nil
}

// marshalAndWrite serializes src to YAML and hands off to WriteFileAtomic.
// Parent directories are created with mode 0755 so the first write on a
// fresh machine succeeds without a separate MkdirAll dance.
func marshalAndWrite(path string, src any, mode os.FileMode) error {
	buf, err := yaml.Marshal(src)
	if err != nil {
		return fmt.Errorf("config: marshal: %w", err)
	}
	return WriteFileAtomic(path, buf, mode)
}

// WriteFileAtomic writes data to path atomically: a sibling temp file is
// written, fsync'd, and renamed into place. Callers observe either the old
// content or the new content, never a partial write. Parent directories are
// created with mode 0755 if absent.
func WriteFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("config: mkdir %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("config: create temp in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	// If anything past this point fails, remove the orphan temp file.
	cleanup := func() { _ = os.Remove(tmpPath) }

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("config: write %s: %w", tmpPath, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("config: chmod %s: %w", tmpPath, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("config: sync %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("config: close %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return fmt.Errorf("config: rename %s → %s: %w", tmpPath, path, err)
	}
	return nil
}
