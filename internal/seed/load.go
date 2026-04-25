package seed

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// ErrNotFound is returned when neither the built-in registry nor the
// garage seeds directory has a template with the given name. Callers
// surface this as `seed_not_found` to clients.
var ErrNotFound = errors.New("seed template not found")

// Load resolves a seed template by name. Built-ins take priority, then
// `<dir>/<name>.yaml`. `dir` may be empty — in that case only built-ins
// are consulted.
func Load(name, dir string) (*Template, error) {
	if t, ok := builtins[name]; ok {
		// Return a copy so callers can't mutate the registry.
		out := t
		out.Files = append([]File(nil), t.Files...)
		return &out, nil
	}
	if dir == "" {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, name)
	}
	path := filepath.Join(dir, name+".yaml")
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, name)
	}
	if err != nil {
		return nil, fmt.Errorf("read seed %q: %w", path, err)
	}
	var t Template
	if err := yaml.Unmarshal(raw, &t); err != nil {
		return nil, fmt.Errorf("parse seed %q: %w", path, err)
	}
	t.Name = name
	if err := t.Validate(); err != nil {
		return nil, fmt.Errorf("seed %q: %w", name, err)
	}
	return &t, nil
}

// Validate checks structural invariants the YAML loader can't enforce.
// Built-ins are trusted and skip this; only disk templates run through it.
func (t *Template) Validate() error {
	if len(t.Files) == 0 {
		return errors.New("template has no files")
	}
	for i, f := range t.Files {
		if f.Path == "" {
			return fmt.Errorf("file[%d]: path is required", i)
		}
		if f.Content == "" {
			return fmt.Errorf("file[%d] %q: content is required", i, f.Path)
		}
		if f.OnConflict != "" {
			switch f.OnConflict {
			case ConflictOverwrite, ConflictSkip, ConflictMerge,
				ConflictAppend, ConflictPrepend:
				// ok
			default:
				return fmt.Errorf("file[%d] %q: unknown on_conflict %q (want one of: overwrite, skip, merge, append, prepend)",
					i, f.Path, f.OnConflict)
			}
		}
	}
	return nil
}
