package server

import (
	"context"
	"encoding/json"

	"github.com/kurisu-agent/drift/internal/devpod"
	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/wire"
)

// KartMigrateDeps wires the migrate handlers. Reuses KartDeps for the
// garage walk + devpod client, and embeds *Deps so the server config
// loader is available without a second wiring site; AgentRoot lets tests
// point scans at a tmpdir instead of the real ~/.devpod/agent/contexts/.
type KartMigrateDeps struct {
	KartDeps
	// Server: optional — when non-nil, default_tune / default_character
	// are surfaced for the migrate CLI's dropdown pre-selection. nil skips
	// the echo and clients fall back to no pre-selection.
	Server *Deps
	// AgentRoot: empty falls back to devpod.AgentContextsRoot().
	AgentRoot string
}

// KartMigrateCandidate is one row in the migrate picker. Name + Context
// together identify the source devpod workspace; Repo is the git URL
// drift migrate will pass to kart.new as --clone.
type KartMigrateCandidate struct {
	Name    string `json:"name"`
	Context string `json:"context"`
	Repo    string `json:"repo"`
}

// KartMigrateListResult wraps the candidate slice so top-level fields
// (counts, notes) can be added later without breaking clients.
// DefaultTune and DefaultCharacter echo the server config so the migrate
// CLI can pre-select a dropdown value without a second round trip; they
// may legitimately be empty on a freshly-initialized server.
type KartMigrateListResult struct {
	Candidates       []KartMigrateCandidate `json:"candidates"`
	DefaultTune      string                 `json:"default_tune,omitempty"`
	DefaultCharacter string                 `json:"default_character,omitempty"`
}

func RegisterKartMigrate(reg *rpc.Registry, d KartMigrateDeps) {
	reg.Register(wire.MethodKartMigrateList, d.kartMigrateListHandler)
}

func (d KartMigrateDeps) kartMigrateListHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p struct{}
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	root := d.AgentRoot
	if root == "" {
		root = devpod.AgentContextsRoot()
	}
	entries, err := devpod.ListAgentWorkspaces(root)
	if err != nil {
		return nil, rpcerr.Internal("kart.migrate_list: %v", err).Wrap(err)
	}
	garage, err := d.listGarageKarts()
	if err != nil {
		return nil, err
	}

	// Build the "already migrated" set keyed by (context, name) so a kart
	// renamed after migration still hides its source workspace on the
	// next run. Context+Name pairs are unique within devpod's agent tree,
	// so a simple delimiter join is collision-free.
	migrated := make(map[string]struct{}, len(garage))
	for _, cfg := range garage {
		if cfg.MigratedFrom == nil || cfg.MigratedFrom.IsZero() {
			continue
		}
		migrated[migrateKey(cfg.MigratedFrom.Context, cfg.MigratedFrom.Name)] = struct{}{}
	}

	out := make([]KartMigrateCandidate, 0, len(entries))
	for _, e := range entries {
		// Non-git workspaces can't be reproduced deterministically via
		// kart.new (which takes a clone URL). Skip silently — a
		// diagnostic in the migrate CLI covers the user-facing message.
		if e.Workspace.Source.GitRepository == "" {
			continue
		}
		// Drift-managed karts share the agent contexts tree with
		// user-owned workspaces until the drift-context switch lands.
		// A matching garage entry means this workspace already belongs
		// to drift, not a migration candidate.
		if _, inGarage := garage[e.Workspace.ID]; inGarage {
			continue
		}
		// Already-migrated dedup by back-reference.
		if _, already := migrated[migrateKey(e.Context, e.Workspace.ID)]; already {
			continue
		}
		out = append(out, KartMigrateCandidate{
			Name:    e.Workspace.ID,
			Context: e.Context,
			Repo:    e.Workspace.Source.GitRepository,
		})
	}

	// Server defaults for the dropdown pre-selection. Missing config is
	// tolerated — an uninitialized server returns empty defaults and the
	// CLI falls back to no pre-selection. Falls back to a temporary
	// *Deps built from d.GarageDir when Server wasn't wired so existing
	// tests that only populate KartDeps keep passing.
	var defaultTune, defaultCharacter string
	srvDeps := d.Server
	if srvDeps == nil && d.GarageDir != "" {
		srvDeps = &Deps{GarageDir: d.GarageDir}
	}
	if srvDeps != nil {
		if srv, err := srvDeps.LoadServerConfig(); err == nil {
			defaultTune = srv.DefaultTune
			defaultCharacter = srv.DefaultCharacter
		}
	}
	return KartMigrateListResult{
		Candidates:       out,
		DefaultTune:      defaultTune,
		DefaultCharacter: defaultCharacter,
	}, nil
}

// migrateKey joins (context, name) with a delimiter that can't appear in
// either field per devpod's own name validation.
func migrateKey(ctx, name string) string { return ctx + "\x00" + name }
