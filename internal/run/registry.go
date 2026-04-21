package run

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"sort"

	"gopkg.in/yaml.v3"
)

// Entry is a single registry item. Command is a Go text/template string
// rendered via [Render].
type Entry struct {
	Name        string   `yaml:"-"`
	Description string   `yaml:"description,omitempty"`
	Mode        Mode     `yaml:"mode"`
	Post        PostHook `yaml:"post,omitempty"`
	Command     string   `yaml:"command"`
}

// Registry is the parsed runs.yaml file. The map preserves the
// "runs.yaml is the source of truth" shape; [Sorted] returns entries in a
// stable order for listings.
type Registry struct {
	Entries map[string]Entry
}

type fileShape struct {
	Runs map[string]Entry `yaml:"runs"`
}

var nameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)

// Load reads and parses a runs.yaml file. A missing file returns an empty
// registry, not an error — a circuit that hasn't yet been initialized
// should report "no runs" instead of surfacing a filesystem error.
func Load(path string) (*Registry, error) {
	buf, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &Registry{Entries: map[string]Entry{}}, nil
		}
		return nil, fmt.Errorf("run: read %s: %w", path, err)
	}
	return Parse(buf)
}

// Parse validates shape + names so later template errors refer to
// commands that definitely exist by the same name they're registered
// under.
func Parse(buf []byte) (*Registry, error) {
	var f fileShape
	if err := yaml.Unmarshal(buf, &f); err != nil {
		return nil, fmt.Errorf("run: parse yaml: %w", err)
	}
	reg := &Registry{Entries: make(map[string]Entry, len(f.Runs))}
	for name, e := range f.Runs {
		if !nameRE.MatchString(name) {
			return nil, fmt.Errorf("run: invalid entry name %q (must match %s)", name, nameRE.String())
		}
		switch e.Mode {
		case ModeInteractive, ModeOutput:
		case "":
			return nil, fmt.Errorf("run: entry %q: mode required", name)
		default:
			return nil, fmt.Errorf("run: entry %q: unknown mode %q", name, e.Mode)
		}
		if e.Command == "" {
			return nil, fmt.Errorf("run: entry %q: command required", name)
		}
		if e.Post != PostNone && e.Post != PostConnectLastScaffold {
			return nil, fmt.Errorf("run: entry %q: unknown post hook %q", name, e.Post)
		}
		e.Name = name
		reg.Entries[name] = e
	}
	return reg, nil
}

// Get returns the entry for name; ok=false means no such entry.
func (r *Registry) Get(name string) (Entry, bool) {
	e, ok := r.Entries[name]
	return e, ok
}

// Sorted returns entries in name order. Used for listings.
func (r *Registry) Sorted() []Entry {
	out := make([]Entry, 0, len(r.Entries))
	for _, e := range r.Entries {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
