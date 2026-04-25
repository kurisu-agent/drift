// Package ports manages drift's workstation-side TCP port forwards: a
// state file (~/.config/drift/ports.yaml) listing every forward the user
// wants kept alive, plus a live-state cache and reconcile loop that drives
// per-kart ssh ControlMasters via `ssh -O check / -O forward / -O cancel`.
//
// The "sidecar" is a discipline, not a process: drift owns the master
// lifecycle (one master per kart with active forwards), persistence comes
// from the state file, and reconcile is invoked synchronously from
// `drift connect` and `drift ports`. Plan 13 has the full design.
package ports

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/kurisu-agent/drift/internal/config"
)

// CurrentVersion is the on-disk schema version for ports.yaml. Pre-1.0 we
// don't migrate; a future bump will reject older files outright.
const CurrentVersion = 1

// Source classifies how a forward got into the state file. User-explicit
// forwards take precedence over devcontainer passthrough — when the same
// (kart, remote) appears from both sources, an existing explicit entry is
// preserved as-is by reconcile/union.
type Source string

const (
	SourceExplicit     Source = "explicit"
	SourceDevcontainer Source = "devcontainer"
	SourceAuto         Source = "auto"
)

// Forward is one workstation-side listener bound to a kart-side port.
// Local is the workstation port; Remote is the in-kart port. RemappedFrom
// is set only when conflict resolution moved Local off the originally
// requested port (typically Remote). Source records the origin.
type Forward struct {
	Local        int    `yaml:"local"`
	Remote       int    `yaml:"remote"`
	RemappedFrom int    `yaml:"remapped_from,omitempty"`
	Source       Source `yaml:"source,omitempty"`
}

// State is the parsed shape of ~/.config/drift/ports.yaml. The map key is
// "<circuit>/<kart>"; per-kart slices stay sorted by Remote for stable
// diffs and stable rendering.
type State struct {
	Version  int                  `yaml:"version"`
	Forwards map[string][]Forward `yaml:"forwards,omitempty"`
}

// KartKey builds the map key used in State.Forwards.
func KartKey(circuit, kart string) string {
	return circuit + "/" + kart
}

// SplitKartKey is the inverse of KartKey. Returns ok=false when the key
// is not exactly one slash, which never happens for keys we wrote.
func SplitKartKey(k string) (circuit, kart string, ok bool) {
	idx := strings.IndexByte(k, '/')
	if idx <= 0 || idx == len(k)-1 {
		return "", "", false
	}
	if strings.IndexByte(k[idx+1:], '/') >= 0 {
		return "", "", false
	}
	return k[:idx], k[idx+1:], true
}

// SSHHost is the ssh_config alias drift's wildcard block matches. Keep in
// sync with sshconf.WildcardHost ("drift.*.*").
func SSHHost(circuit, kart string) string {
	return "drift." + circuit + "." + kart
}

// DefaultPath returns ~/.config/drift/ports.yaml (XDG_CONFIG_HOME aware).
func DefaultPath() (string, error) {
	dir, err := config.ClientConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ports.yaml"), nil
}

// Load reads ports.yaml. A missing file returns an empty State (not an
// error) so callers can treat absence as "no forwards yet".
func Load(path string) (*State, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &State{Version: CurrentVersion}, nil
		}
		return nil, fmt.Errorf("ports: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	var s State
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("ports: decode %s: %w", path, err)
	}
	if s.Version == 0 {
		s.Version = CurrentVersion
	}
	if s.Version != CurrentVersion {
		return nil, fmt.Errorf("ports: %s: unsupported version %d (want %d)", path, s.Version, CurrentVersion)
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	s.normalize()
	return &s, nil
}

// Save writes ports.yaml at 0600 via WriteFileAtomic. Sorts per-kart
// slices and prunes empty entries before marshalling.
func Save(path string, s *State) error {
	if err := s.Validate(); err != nil {
		return err
	}
	s.Version = CurrentVersion
	s.normalize()
	buf, err := yaml.Marshal(s)
	if err != nil {
		return fmt.Errorf("ports: marshal: %w", err)
	}
	return config.WriteFileAtomic(path, buf, 0o600)
}

// Validate checks invariants reconcile depends on: ports in [1, 65535],
// kart keys well-formed, no duplicate (Local) on the same kart.
func (s *State) Validate() error {
	for key, fwds := range s.Forwards {
		if _, _, ok := SplitKartKey(key); !ok {
			return fmt.Errorf("ports: invalid kart key %q (want '<circuit>/<kart>')", key)
		}
		seen := make(map[int]bool, len(fwds))
		for i := range fwds {
			f := &fwds[i]
			if f.Local < 1 || f.Local > 65535 {
				return fmt.Errorf("ports: %s: local port out of range: %d", key, f.Local)
			}
			if f.Remote < 1 || f.Remote > 65535 {
				return fmt.Errorf("ports: %s: remote port out of range: %d", key, f.Remote)
			}
			if seen[f.Local] {
				return fmt.Errorf("ports: %s: duplicate local port %d", key, f.Local)
			}
			seen[f.Local] = true
		}
	}
	return nil
}

func (s *State) normalize() {
	for key, fwds := range s.Forwards {
		if len(fwds) == 0 {
			delete(s.Forwards, key)
			continue
		}
		sort.Slice(fwds, func(i, j int) bool {
			if fwds[i].Remote != fwds[j].Remote {
				return fwds[i].Remote < fwds[j].Remote
			}
			return fwds[i].Local < fwds[j].Local
		})
		s.Forwards[key] = fwds
	}
}

// Get returns the forwards for a kart (nil-safe; returns a fresh empty
// slice if no entry exists).
func (s *State) Get(circuit, kart string) []Forward {
	if s == nil || s.Forwards == nil {
		return nil
	}
	return s.Forwards[KartKey(circuit, kart)]
}

// Put replaces the forwards for a kart with a non-empty slice. Use
// Delete for the remove-kart path — passing nil / an empty slice here
// is treated as a delete (for symmetry with Get), but callers
// expressing "remove this kart from the state" should prefer Delete
// for intent clarity.
func (s *State) Put(circuit, kart string, fwds []Forward) {
	if len(fwds) == 0 {
		s.Delete(circuit, kart)
		return
	}
	if s.Forwards == nil {
		s.Forwards = make(map[string][]Forward)
	}
	s.Forwards[KartKey(circuit, kart)] = fwds
}

// Delete removes all forwards for a kart. No-op if the kart has no
// entry.
func (s *State) Delete(circuit, kart string) {
	if s == nil || s.Forwards == nil {
		return
	}
	delete(s.Forwards, KartKey(circuit, kart))
}

// Find returns the index of a forward with matching Remote, or -1.
func Find(fwds []Forward, remote int) int {
	for i := range fwds {
		if fwds[i].Remote == remote {
			return i
		}
	}
	return -1
}

// FindByLocal returns the index of a forward with matching Local, or -1.
func FindByLocal(fwds []Forward, local int) int {
	for i := range fwds {
		if fwds[i].Local == local {
			return i
		}
	}
	return -1
}
