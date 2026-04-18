package server

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/kurisu-agent/drift/internal/config"
	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"gopkg.in/yaml.v3"
)

// Tune mirrors the on-disk shape at `garage/tunes/<name>.yaml`. All fields
// are optional — tunes exist to compose defaults at `drift new` time.
type Tune struct {
	Starter      string `yaml:"starter,omitempty" json:"starter,omitempty"`
	Devcontainer string `yaml:"devcontainer,omitempty" json:"devcontainer,omitempty"`
	DotfilesRepo string `yaml:"dotfiles_repo,omitempty" json:"dotfiles_repo,omitempty"`
	Features     string `yaml:"features,omitempty" json:"features,omitempty"`
}

// TuneResult is the JSON shape returned by list/show/set — the tune with its
// name spliced in so renderers don't need to key the map themselves.
type TuneResult struct {
	Name string `json:"name"`
	Tune
}

// TuneSetParams is the RPC param shape for `tune.set`. The fields match
// Tune directly; name is validated separately.
type TuneSetParams struct {
	Name         string `json:"name"`
	Starter      string `json:"starter,omitempty"`
	Devcontainer string `json:"devcontainer,omitempty"`
	DotfilesRepo string `json:"dotfiles_repo,omitempty"`
	Features     string `json:"features,omitempty"`
}

// TuneNameOnly is the param shape for tune.show / tune.remove.
type TuneNameOnly struct {
	Name string `json:"name"`
}

// tuneNameRE is deliberately local to this package — unlike characters and
// karts, `default` is a *legitimate* tune name (the literal tune named
// `default` is what `--tune default` resolves to). Only `none` is reserved,
// since it's the sentinel for "no tune at all".
var tuneNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

func validateTuneName(n string) error {
	if n == "none" {
		return rpcerr.UserError(rpcerr.TypeInvalidName,
			"tune name %q is reserved", n).With("name", n)
	}
	if !tuneNameRE.MatchString(n) {
		return rpcerr.UserError(rpcerr.TypeInvalidName,
			"tune name %q is invalid (must match %s)", n, tuneNameRE.String()).
			With("name", n).With("pattern", tuneNameRE.String())
	}
	return nil
}

// TuneListHandler enumerates `garage/tunes/*.yaml`.
func (d *Deps) TuneListHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p struct{}
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	dir := d.tuneDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []TuneResult{}, nil
		}
		return nil, rpcerr.Internal("tune.list: %v", err).Wrap(err)
	}
	out := make([]TuneResult, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		n := strings.TrimSuffix(e.Name(), ".yaml")
		t, err := d.loadTune(n)
		if err != nil {
			return nil, err
		}
		out = append(out, TuneResult{Name: n, Tune: *t})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// TuneShowHandler returns a single tune.
func (d *Deps) TuneShowHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p TuneNameOnly
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	if p.Name == "" {
		return nil, rpcerr.UserError(rpcerr.TypeInvalidFlag, "tune.show: name is required")
	}
	t, err := d.loadTune(p.Name)
	if err != nil {
		return nil, err
	}
	return TuneResult{Name: p.Name, Tune: *t}, nil
}

// TuneSetHandler creates or updates a tune — `tune.set` is idempotent
// ("creates or updates").
func (d *Deps) TuneSetHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p TuneSetParams
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	if err := validateTuneName(p.Name); err != nil {
		return nil, err
	}
	t := Tune{
		Starter:      p.Starter,
		Devcontainer: p.Devcontainer,
		DotfilesRepo: p.DotfilesRepo,
		Features:     p.Features,
	}
	buf, err := yaml.Marshal(&t)
	if err != nil {
		return nil, rpcerr.Internal("tune.set: marshal: %v", err).Wrap(err)
	}
	if err := config.WriteFileAtomic(d.tunePath(p.Name), buf, 0o644); err != nil {
		return nil, rpcerr.Internal("tune.set: %v", err).Wrap(err)
	}
	return TuneResult{Name: p.Name, Tune: t}, nil
}

// TuneRemoveHandler deletes a tune file. Rejects with `code:4` if any kart
// references it, mirroring character.remove.
func (d *Deps) TuneRemoveHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p TuneNameOnly
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	if p.Name == "" {
		return nil, rpcerr.UserError(rpcerr.TypeInvalidFlag, "tune.remove: name is required")
	}
	path := d.tunePath(p.Name)
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, rpcerr.NotFound(typeTuneNotFound,
				"tune %q not found", p.Name).With("name", p.Name)
		}
		return nil, rpcerr.Internal("tune.remove: stat %s: %v", path, err).Wrap(err)
	}
	used, err := d.kartsReferencing("tune", p.Name)
	if err != nil {
		return nil, err
	}
	if len(used) > 0 {
		return nil, rpcerr.Conflict(rpcerr.TypeNameCollision,
			"tune %q is referenced by kart(s): %s", p.Name, strings.Join(used, ", ")).
			With("name", p.Name).With("karts", used)
	}
	if err := os.Remove(path); err != nil {
		return nil, rpcerr.Internal("tune.remove: %v", err).Wrap(err)
	}
	return TuneNameOnly{Name: p.Name}, nil
}

func (d *Deps) tuneDir() string {
	g, _ := d.garageDir()
	return filepath.Join(g, "tunes")
}
func (d *Deps) tunePath(n string) string { return filepath.Join(d.tuneDir(), n+".yaml") }

func (d *Deps) loadTune(n string) (*Tune, error) {
	path := d.tunePath(n)
	buf, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, rpcerr.NotFound(typeTuneNotFound,
				"tune %q not found", n).With("name", n)
		}
		return nil, rpcerr.Internal("tune: %v", err).Wrap(err)
	}
	var t Tune
	if err := yaml.Unmarshal(buf, &t); err != nil {
		return nil, rpcerr.Internal("tune: decode %s: %v", path, err).Wrap(err)
	}
	return &t, nil
}

// typeTuneNotFound is a local error type — tune-specific not-found. The
// canonical set in rpcerr covers devpod-adjacent resources; tunes are
// file-backed and exclusive to this package, so a local constant avoids
// widening the shared enum for a single Phase 6 case.
const typeTuneNotFound = rpcerr.Type("tune_not_found")
