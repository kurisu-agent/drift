package server

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/kurisu-agent/drift/internal/chest"
	"github.com/kurisu-agent/drift/internal/kart"
	"github.com/kurisu-agent/drift/internal/model"
	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/wire"
)

// KartNewDeps is split from KartDeps so kart.new can evolve independently
// from the lifecycle handlers.
type KartNewDeps struct {
	Deps *Deps
	// Kart: the handler overrides Resolver and GarageDir at call time.
	// Tests pre-populate Devpod/Starter/Fetcher/Clock here.
	Kart kart.NewDeps
}

// KartNewParams field names mirror `drift new` flags so drift and lakitu
// schemas align without translation.
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

func RegisterKartNew(reg *rpc.Registry, kd KartNewDeps) {
	reg.Register(wire.MethodKartNew, kd.kartNewHandler)
}

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
		LoadTune: func(name string) (*model.Tune, error) {
			return kd.Deps.loadTune(name)
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

	// Preserve caller-pre-populated fields (devpod, starter, fetcher, clock)
	// while overriding the garage-dependent ones.
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
// the layer-1 dotfiles generator embeds. Empty input returns empty output.
// Non-chest-prefixed values are rejected — character.add enforces the shape.
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

// openChestBackend exists separately from Deps.openChest to avoid colliding
// with the private method already defined in server.go.
func (kd KartNewDeps) openChestBackend() (chest.Backend, error) {
	if kd.Deps == nil {
		return nil, rpcerr.Internal("kart.new: deps not configured")
	}
	return kd.Deps.openChest()
}
