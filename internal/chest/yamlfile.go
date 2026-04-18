package chest

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/kurisu-agent/drift/internal/config"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"gopkg.in/yaml.v3"
)

// YAMLFileBackend stores secrets as a single YAML map under the garage. The
// file mode is always 0600; multi-line values ride on YAML's `|` block
// scalars automatically (yaml.v3 picks the style based on the value), so
// PEM-encoded PATs and SSH keys round-trip through `chest set` / `chest
// get` without the escaping dance the earlier shell-quoted backend needed.
type YAMLFileBackend struct {
	path string
}

// NewYAMLFile returns the canonical YAML-backed chest that writes to
// <garageDir>/chest/secrets.yaml.
func NewYAMLFile(garageDir string) *YAMLFileBackend {
	return &YAMLFileBackend{path: filepath.Join(garageDir, "chest", "secrets.yaml")}
}

// Path returns the absolute path to the secrets file. Exposed for
// diagnostics and tests.
func (b *YAMLFileBackend) Path() string { return b.path }

// Set stores value under name. The enclosing directory is assumed to exist
// (lakitu init creates it); Set will recreate it if missing so tests can
// exercise the backend without running the full init path first.
func (b *YAMLFileBackend) Set(name string, value []byte) error {
	entries, err := b.read()
	if err != nil {
		return err
	}
	entries[name] = string(value)
	return b.write(entries)
}

// Get returns the value stored under name. Missing keys yield a not-found
// rpcerr so callers can match on Type without sniffing errno.
func (b *YAMLFileBackend) Get(name string) ([]byte, error) {
	entries, err := b.read()
	if err != nil {
		return nil, err
	}
	v, ok := entries[name]
	if !ok {
		return nil, notFound(name)
	}
	return []byte(v), nil
}

// List returns the stored key names in lexicographic order. Values are
// never returned.
func (b *YAMLFileBackend) List() ([]string, error) {
	entries, err := b.read()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for k := range entries {
		names = append(names, k)
	}
	sort.Strings(names)
	return names, nil
}

// Remove deletes the entry stored under name. Missing keys yield a
// not-found rpcerr.
func (b *YAMLFileBackend) Remove(name string) error {
	entries, err := b.read()
	if err != nil {
		return err
	}
	if _, ok := entries[name]; !ok {
		return notFound(name)
	}
	delete(entries, name)
	return b.write(entries)
}

func notFound(name string) error {
	return rpcerr.New(rpcerr.CodeNotFound, rpcerr.TypeChestEntryNotFound,
		"chest entry %q not found", name).With("name", name)
}

func (b *YAMLFileBackend) read() (map[string]string, error) {
	out := make(map[string]string)
	buf, err := os.ReadFile(b.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return out, nil
		}
		return nil, fmt.Errorf("chest: open %s: %w", b.path, err)
	}
	if len(buf) == 0 {
		return out, nil
	}
	if err := yaml.Unmarshal(buf, &out); err != nil {
		return nil, fmt.Errorf("chest: parse %s: %w", b.path, err)
	}
	return out, nil
}

func (b *YAMLFileBackend) write(entries map[string]string) error {
	// Ensure the chest directory exists with mode 0700 — WriteFileAtomic
	// creates parent dirs at 0755, but the chest subtree is 0700.
	if err := os.MkdirAll(filepath.Dir(b.path), 0o700); err != nil {
		return fmt.Errorf("chest: mkdir %s: %w", filepath.Dir(b.path), err)
	}
	// yaml.v3 emits `|` or `|-` block scalars automatically when a value
	// contains newlines, so multi-line secrets survive a round-trip with
	// no extra encoding.
	buf, err := yaml.Marshal(&entries)
	if err != nil {
		return fmt.Errorf("chest: marshal: %w", err)
	}
	if len(entries) == 0 {
		// yaml.v3 marshals an empty map as `{}\n`; rewrite to an empty
		// document so `cat secrets.yaml` on a cleared chest isn't noisy.
		buf = []byte{}
	}
	return config.WriteFileAtomic(b.path, buf, 0o600)
}

// Compile-time guard that YAMLFileBackend satisfies the package interface.
var _ Backend = (*YAMLFileBackend)(nil)
