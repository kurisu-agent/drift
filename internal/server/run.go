package server

import (
	"context"
	"encoding/json"
	"path/filepath"

	"github.com/kurisu-agent/drift/internal/config"
	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/run"
	"github.com/kurisu-agent/drift/internal/wire"
)

// typeRunNotFound mirrors the package-local convention used by tune.go —
// specific enough for programmatic branching, no need to pollute
// rpcerr's shared type catalog.
const typeRunNotFound = rpcerr.Type("run_not_found")

// RunListHandler returns the metadata for every entry; omits command
// strings so a list call stays cheap and doesn't leak resolvable
// shorthand shapes the client isn't supposed to interpret.
func (d *Deps) RunListHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p struct{}
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	reg, err := d.loadRunRegistry()
	if err != nil {
		return nil, err
	}
	entries := reg.Sorted()
	out := wire.RunListResult{Entries: make([]wire.RunEntry, 0, len(entries))}
	for _, e := range entries {
		out.Entries = append(out.Entries, wire.RunEntry{
			Name:        e.Name,
			Description: e.Description,
			Mode:        e.Mode,
			Post:        e.Post,
			Args:        e.Args,
		})
	}
	return out, nil
}

// RunResolveHandler expands a single entry's template with caller-supplied
// args. The rendered command is what the client actually ssh/mosh's.
func (d *Deps) RunResolveHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p wire.RunResolveParams
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	if p.Name == "" {
		return nil, rpcerr.UserError(rpcerr.TypeInvalidFlag, "run.resolve: name is required")
	}
	reg, err := d.loadRunRegistry()
	if err != nil {
		return nil, err
	}
	entry, ok := reg.Get(p.Name)
	if !ok {
		return nil, rpcerr.NotFound(typeRunNotFound, "run %q not found", p.Name).With("name", p.Name)
	}
	rendered, err := run.Render(entry.Command, p.Args)
	if err != nil {
		return nil, rpcerr.Internal("run.resolve %q: %v", p.Name, err).With("name", p.Name).Wrap(err)
	}
	return wire.RunResolveResult{
		Name:    entry.Name,
		Mode:    entry.Mode,
		Post:    entry.Post,
		Command: rendered,
	}, nil
}

func (d *Deps) loadRunRegistry() (*run.Registry, error) {
	home, err := d.driftHome()
	if err != nil {
		return nil, rpcerr.Internal("run: resolve drift home: %v", err).Wrap(err)
	}
	reg, err := run.Load(filepath.Join(home, "runs.yaml"))
	if err != nil {
		return nil, rpcerr.Internal("run: load registry: %v", err).Wrap(err)
	}
	// Back-fill args: declarations onto built-in entries that were seeded
	// by an older lakitu (pre-v0.5.2) and so lack the prompt metadata the
	// client needs. Only untouched-command entries get the merge — any
	// user customization opts the entry out.
	if defaults, derr := run.Parse(config.DefaultRunsYAML()); derr == nil {
		run.MergeBuiltinDefaults(reg, defaults)
	}
	return reg, nil
}
