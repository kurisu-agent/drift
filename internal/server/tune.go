package server

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kurisu-agent/drift/internal/chest"
	"github.com/kurisu-agent/drift/internal/config"
	"github.com/kurisu-agent/drift/internal/model"
	"github.com/kurisu-agent/drift/internal/name"
	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"gopkg.in/yaml.v3"
)

// Tune is the on-disk tune shape. Kept as a local alias of model.Tune
// so external callers (tests, CLI glue) that reference server.Tune
// continue to compile. All fields optional — tunes compose defaults at
// `drift new` time.
type Tune = model.Tune

// TuneResult splices the name in so renderers don't need to key the map.
type TuneResult struct {
	Name string `json:"name"`
	Tune
}

type TuneSetParams struct {
	Name         string        `json:"name"`
	Starter      string        `json:"starter,omitempty"`
	Devcontainer string        `json:"devcontainer,omitempty"`
	DotfilesRepo string        `json:"dotfiles_repo,omitempty"`
	Features     string        `json:"features,omitempty"`
	Env          model.TuneEnv `json:"env,omitempty"`
	MountDirs    []model.Mount `json:"mount_dirs,omitempty"`
}

type TuneNameOnly struct {
	Name string `json:"name"`
}

func (d *Deps) TuneListHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p struct{}
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	names, err := listYAMLNames(d.tuneDir())
	if err != nil {
		return nil, rpcerr.Internal("tune.list: %v", err).Wrap(err)
	}
	out := make([]TuneResult, 0, len(names))
	for _, n := range names {
		t, err := d.loadTune(n)
		if err != nil {
			return nil, err
		}
		out = append(out, TuneResult{Name: n, Tune: *t})
	}
	return out, nil
}

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

// TuneSetHandler is idempotent — creates or updates.
func (d *Deps) TuneSetHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p TuneSetParams
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	if err := name.ValidateAllowing("tune", p.Name, "default"); err != nil {
		return nil, err
	}
	if err := validateTuneEnv(p.Env); err != nil {
		return nil, err
	}
	t := Tune{
		Starter:      p.Starter,
		Devcontainer: p.Devcontainer,
		DotfilesRepo: p.DotfilesRepo,
		Features:     p.Features,
		Env:          p.Env,
		MountDirs:    p.MountDirs,
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

// TuneRemoveHandler rejects with code:4 if any kart references the tune
// (mirrors character.remove).
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

// Local constant — tunes are file-backed and exclusive to this package,
// so the canonical rpcerr enum isn't widened for a single case.
const typeTuneNotFound = rpcerr.Type("tune_not_found")

// validateTuneEnv enforces the chest-only invariant: every value across
// every block must start with `chest:`. Mirrors character.add's PATSecret
// check so literal secrets never land on disk outside the chest.
func validateTuneEnv(e model.TuneEnv) error {
	// Block order matches the struct definition so error messages are
	// stable; map iteration is sorted per block for the same reason.
	blocks := []struct {
		name string
		m    map[string]string
	}{
		{"build", e.Build},
		{"workspace", e.Workspace},
		{"session", e.Session},
	}
	for _, b := range blocks {
		if len(b.m) == 0 {
			continue
		}
		keys := make([]string, 0, len(b.m))
		for k := range b.m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := b.m[k]
			if !strings.HasPrefix(v, chest.RefPrefix) {
				return rpcerr.UserError(rpcerr.TypeInvalidFlag,
					"tune.set: env.%s.%s must be a chest reference of the form %q; literal values are not accepted",
					b.name, k, chest.RefPrefix+"<name>").
					With("block", b.name).With("key", k)
			}
		}
	}
	return nil
}
