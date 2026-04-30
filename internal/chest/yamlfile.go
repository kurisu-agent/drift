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

// YAMLFileBackend stores secrets as a single 0600 YAML map. yaml.v3
// auto-picks block scalars for multi-line values, so PEM PATs and SSH
// keys round-trip without the escaping dance the earlier shell-quoted
// backend needed.
type YAMLFileBackend struct {
	path string
}

func NewYAMLFile(garageDir string) *YAMLFileBackend {
	return &YAMLFileBackend{path: filepath.Join(garageDir, "chest", "secrets.yaml")}
}

func (b *YAMLFileBackend) Path() string { return b.path }

func (b *YAMLFileBackend) Set(name string, value []byte) error {
	entries, err := b.read()
	if err != nil {
		return err
	}
	entries[name] = string(value)
	return b.write(entries)
}

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
	// WriteFileAtomic creates parents at 0755 but the chest subtree is 0700.
	if err := os.MkdirAll(filepath.Dir(b.path), 0o700); err != nil {
		return fmt.Errorf("chest: mkdir %s: %w", filepath.Dir(b.path), err)
	}
	buf, err := yaml.Marshal(&entries)
	if err != nil {
		return fmt.Errorf("chest: marshal: %w", err)
	}
	if len(entries) == 0 {
		// yaml.v3 marshals an empty map as `{}\n`; rewrite to empty so
		// `cat secrets.yaml` on a cleared chest isn't noisy.
		buf = []byte{}
	}
	return config.WriteFileAtomic(b.path, buf, 0o600)
}

var _ Backend = (*YAMLFileBackend)(nil)
