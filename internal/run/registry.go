package run

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"sort"

	"github.com/kurisu-agent/drift/internal/wire"
	"gopkg.in/yaml.v3"
)

// Entry is a single registry item. Command is a Go text/template string
// rendered via [Render]. Args declares the positional slots the client is
// allowed to prompt for when a user invokes the entry interactively.
type Entry struct {
	Name        string    `yaml:"-"`
	Description string    `yaml:"description,omitempty"`
	Mode        Mode      `yaml:"mode"`
	Post        PostHook  `yaml:"post,omitempty"`
	Args        []ArgSpec `yaml:"args,omitempty"`
	Command     string    `yaml:"command"`
}

// ArgSpec / ArgType alias the wire types so server/callers don't have to
// import wire for values whose whole purpose is to cross the wire.
type (
	ArgSpec = wire.RunArgSpec
	ArgType = wire.RunArgType
)

const (
	ArgTypeInput  = wire.RunArgTypeInput
	ArgTypeText   = wire.RunArgTypeText
	ArgTypeSelect = wire.RunArgTypeSelect
)

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
		if err := validateArgs(name, e.Args); err != nil {
			return nil, err
		}
		e.Name = name
		reg.Entries[name] = e
	}
	return reg, nil
}

var argNameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)

// validateArgs enforces the schema for an entry's prompt declarations:
// names are unique and match argNameRE; type (if set) is one of the known
// widgets; select entries need options; select defaults must be one of
// those options. Non-select entries must not carry options — catching
// that at parse time is cheaper than a confused widget at prompt time.
func validateArgs(entryName string, args []ArgSpec) error {
	seen := make(map[string]struct{}, len(args))
	for i, a := range args {
		if a.Name == "" {
			return fmt.Errorf("run: entry %q: args[%d]: name required", entryName, i)
		}
		if !argNameRE.MatchString(a.Name) {
			return fmt.Errorf("run: entry %q: args[%d]: invalid name %q (must match %s)", entryName, i, a.Name, argNameRE.String())
		}
		if _, dup := seen[a.Name]; dup {
			return fmt.Errorf("run: entry %q: args[%d]: duplicate name %q", entryName, i, a.Name)
		}
		seen[a.Name] = struct{}{}
		switch a.Type {
		case "", ArgTypeInput, ArgTypeText:
			if len(a.Options) != 0 {
				return fmt.Errorf("run: entry %q: arg %q: options only valid for type %q", entryName, a.Name, ArgTypeSelect)
			}
		case ArgTypeSelect:
			if len(a.Options) == 0 {
				return fmt.Errorf("run: entry %q: arg %q: select requires options", entryName, a.Name)
			}
			if a.Default != "" {
				ok := false
				for _, opt := range a.Options {
					if opt == a.Default {
						ok = true
						break
					}
				}
				if !ok {
					return fmt.Errorf("run: entry %q: arg %q: default %q not in options", entryName, a.Name, a.Default)
				}
			}
		default:
			return fmt.Errorf("run: entry %q: arg %q: unknown type %q", entryName, a.Name, a.Type)
		}
	}
	return nil
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
