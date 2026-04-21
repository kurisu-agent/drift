package server

import (
	"context"
	"encoding/json"
	"path/filepath"

	"github.com/kurisu-agent/drift/internal/config"
	"github.com/kurisu-agent/drift/internal/devpod"
	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/wire"
)

// KartMigrateDeps wires the migrate handlers. Reuses KartDeps for the
// garage walk + devpod client; AgentRoot lets tests point scans at a
// tmpdir instead of the real ~/.devpod/agent/contexts/.
type KartMigrateDeps struct {
	KartDeps
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

// KartMigrateDeleteOldParams targets one pre-migrate devpod workspace.
// Both fields are required; the server refuses a delete targeting
// drift's own context so a misrouted call can't nuke a drift kart.
type KartMigrateDeleteOldParams struct {
	Context string `json:"context"`
	Name    string `json:"name"`
}

func RegisterKartMigrate(reg *rpc.Registry, d KartMigrateDeps) {
	reg.Register(wire.MethodKartMigrateList, d.kartMigrateListHandler)
	reg.Register(wire.MethodKartMigrateDeleteOld, d.kartMigrateDeleteOldHandler)
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
	// CLI falls back to no pre-selection.
	var defaultTune, defaultCharacter string
	if d.GarageDir != "" {
		if srv, err := config.LoadServer(filepath.Join(d.GarageDir, "config.yaml")); err == nil {
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

func (d KartMigrateDeps) kartMigrateDeleteOldHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p KartMigrateDeleteOldParams
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	if p.Name == "" {
		return nil, rpcerr.UserError(rpcerr.TypeInvalidFlag,
			"kart.migrate_delete_old: name is required")
	}
	if p.Context == "" {
		return nil, rpcerr.UserError(rpcerr.TypeInvalidFlag,
			"kart.migrate_delete_old: context is required")
	}
	// Defense in depth: migrate-delete-old is only for the user's
	// pre-drift devpod workspaces. A UI bug that routes a drift kart
	// into this handler must fail loudly instead of silently nuking the
	// kart.
	if p.Context == "drift" {
		return nil, rpcerr.UserError(rpcerr.TypeInvalidFlag,
			"kart.migrate_delete_old: refusing to delete in drift context")
	}
	if d.Devpod == nil {
		return nil, rpcerr.Internal("kart.migrate_delete_old: devpod client not configured")
	}
	// Shallow-copy the client so the context override is scoped to this
	// one call — the server-wide Devpod keeps its original context
	// (empty today, "drift" once the context switch lands).
	cp := *d.Devpod
	cp.Context = p.Context
	if err := cp.Delete(ctx, p.Name); err != nil {
		return nil, rpcerr.New(rpcerr.CodeDevpod, rpcerr.TypeDevpodUpFailed,
			"kart.migrate_delete_old: devpod delete: %v", err).
			Wrap(err).
			With("context", p.Context).
			With("name", p.Name)
	}
	return struct{}{}, nil
}

// migrateKey joins (context, name) with a delimiter that can't appear in
// either field per devpod's own name validation.
func migrateKey(ctx, name string) string { return ctx + "\x00" + name }
