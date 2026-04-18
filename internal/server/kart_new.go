package server

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/kurisu-agent/drift/internal/chest"
	"github.com/kurisu-agent/drift/internal/kart"
	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/wire"
)

// KartNewDeps wires kart.new to its collaborators. Field ownership is split
// with Phase 9's lifecycle handlers (they register kart.start/stop/etc via
// RegisterKartLifecycle). Keeping kart.new's deps separate lets both phases
// evolve independently until a future consolidation.
type KartNewDeps struct {
	// Deps gives kart.new access to the per-circuit config (default tune,
	// default character) and the chest backend (for PAT resolution from
	// character files).
	Deps *Deps
	// Kart is the underlying orchestrator configuration. The handler
	// overrides Kart.Resolver and Kart.GarageDir at call time; tests pre-
	// populate the devpod client and starter here. The zero value is
	// acceptable in production because RegisterKartNew defaults Kart.Devpod.
	Kart kart.NewDeps
}

// KartNewParams is the RPC param shape for kart.new. Field names mirror
// the `drift new` flags so the drift and lakitu schemas align without a
// translation layer.
type KartNewParams struct {
	Name         string `json:"name"`
	Clone        string `json:"clone,omitempty"`
	Starter      string `json:"starter,omitempty"`
	Tune         string `json:"tune,omitempty"`
	Features     string `json:"features,omitempty"`
	Devcontainer string `json:"devcontainer,omitempty"`
	Dotfiles     string `json:"dotfiles,omitempty"`
	Character    string `json:"character,omitempty"`
	Autostart    bool   `json:"autostart,omitempty"`
}

// RegisterKartNew wires kart.new into reg. Split from RegisterKart so
// Phase 9's lifecycle handlers can register in parallel without touching
// the same function. Both phases add one Register call in
// internal/cli/lakitu/lakitu.go.
func RegisterKartNew(reg *rpc.Registry, kd KartNewDeps) {
	reg.Register(wire.MethodKartNew, kd.kartNewHandler)
}

// kartNewHandler parses params, builds a [kart.Resolver] that reads tunes
// and characters from the garage, resolves PAT references via the chest
// backend, and hands the resolved flags to [kart.New].
func (kd KartNewDeps) kartNewHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p KartNewParams
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	if p.Name == "" {
		return nil, rpcerr.UserError(rpcerr.TypeInvalidFlag, "kart.new: name is required")
	}
	if kd.Deps == nil {
		return nil, rpcerr.Internal("kart.new: deps not configured")
	}

	srv, err := kd.Deps.loadServerConfig()
	if err != nil {
		return nil, err
	}

	resolver := &kart.Resolver{
		Defaults: kart.ServerDefaults{
			DefaultTune:      srv.DefaultTune,
			DefaultCharacter: srv.DefaultCharacter,
		},
		LoadTune: func(name string) (*kart.Tune, error) {
			t, err := kd.Deps.loadTune(name)
			if err != nil {
				return nil, err
			}
			return &kart.Tune{
				Starter:      t.Starter,
				Devcontainer: t.Devcontainer,
				DotfilesRepo: t.DotfilesRepo,
				Features:     t.Features,
			}, nil
		},
		LoadCharacter: func(name string) (*kart.Character, error) {
			c, err := kd.Deps.loadCharacter(name)
			if err != nil {
				return nil, err
			}
			pat, err := kd.resolvePATSecret(c.PATSecret)
			if err != nil {
				return nil, err
			}
			return &kart.Character{
				GitName:    c.GitName,
				GitEmail:   c.GitEmail,
				GithubUser: c.GithubUser,
				SSHKeyPath: c.SSHKeyPath,
				PAT:        pat,
			}, nil
		},
	}

	// Preserve whatever the caller pre-populated (devpod client, starter,
	// fetcher, clock) while overriding the garage-dependent fields. Tests
	// pass a fully-prepared kd.Kart; production wiring supplies just the
	// devpod client via Registry() in cli/lakitu.
	kd.Kart.Resolver = resolver
	if kd.Kart.GarageDir == "" {
		garage, derr := kd.Deps.garageDir()
		if derr != nil {
			return nil, rpcerr.Internal("kart.new: %v", derr).Wrap(derr)
		}
		kd.Kart.GarageDir = garage
	}

	f := kart.Flags{
		Name:         p.Name,
		Clone:        p.Clone,
		Starter:      p.Starter,
		Tune:         p.Tune,
		Features:     p.Features,
		Devcontainer: p.Devcontainer,
		Dotfiles:     p.Dotfiles,
		Character:    p.Character,
		Autostart:    p.Autostart,
	}
	return kart.New(ctx, kd.Kart, f)
}

// resolvePATSecret turns a `chest:<name>` reference into the literal token
// the layer-1 dotfiles generator embeds in gh hosts.yml and the git
// credential helper. Empty input returns empty output — the character has
// no PAT attached. Non-chest-prefixed values are rejected; character.add
// enforces the same shape.
func (kd KartNewDeps) resolvePATSecret(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", nil
	}
	if !strings.HasPrefix(ref, "chest:") {
		return "", rpcerr.UserError(rpcerr.TypeInvalidFlag,
			"kart.new: character pat_secret must be a chest:<name> reference")
	}
	key := strings.TrimPrefix(ref, "chest:")
	backend, err := kd.openChestBackend()
	if err != nil {
		return "", err
	}
	val, err := backend.Get(key)
	if err != nil {
		return "", err
	}
	return string(val), nil
}

// openChestBackend delegates to Deps; named uniquely so it doesn't collide
// with the private Deps.openChest method already defined in server.go.
func (kd KartNewDeps) openChestBackend() (chest.Backend, error) {
	if kd.Deps == nil {
		return nil, rpcerr.Internal("kart.new: deps not configured")
	}
	return kd.Deps.openChest()
}
