package server

import (
	"errors"
	"path/filepath"

	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/seed"
)

// seedDir is the on-disk home for user-defined seed templates. Mirrors
// tuneDir / characterDir so the garage layout stays uniform.
func (d *Deps) seedDir() string {
	g, _ := d.garageDir()
	return filepath.Join(g, "seeds")
}

// loadSeed wires kart.Resolver.LoadSeed to the seed package's
// builtin-then-disk lookup, translating the package-level ErrNotFound
// into a structured `seed_not_found` rpcerr the CLI can surface.
func (d *Deps) loadSeed(name string) (*seed.Template, error) {
	t, err := seed.Load(name, d.seedDir())
	if err != nil {
		if errors.Is(err, seed.ErrNotFound) {
			return nil, rpcerr.New(rpcerr.CodeNotFound, rpcerr.TypeSeedNotFound,
				"seed template %q not found (no built-in and no garage/seeds/%s.yaml)",
				name, name).With("name", name)
		}
		return nil, rpcerr.Internal("load seed %q: %v", name, err).Wrap(err)
	}
	return t, nil
}
