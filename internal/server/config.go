package server

import (
	"context"
	"encoding/json"
	"path/filepath"

	"github.com/kurisu-agent/drift/internal/config"
	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/rpcerr"
)

// ConfigSetParams is the shape accepted by `config.set`. Keys use dotted
// paths rooted at the server config (plans/PLAN.md § Server state layout).
type ConfigSetParams struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// ServerConfig is the JSON-shaped mirror of [config.Server]. The config
// package uses yaml-tagged fields for on-disk round-tripping; handlers
// convert to this type before returning so clients see snake_case keys
// matching plans/PLAN.md § Server state layout.
type ServerConfig struct {
	DefaultTune      string      `json:"default_tune"`
	DefaultCharacter string      `json:"default_character"`
	NixCacheURL      string      `json:"nix_cache_url"`
	Chest            ChestConfig `json:"chest"`
}

// ChestConfig is the JSON shape for the `chest:` sub-object.
type ChestConfig struct {
	Backend string `json:"backend"`
}

func toServerConfig(s *config.Server) ServerConfig {
	return ServerConfig{
		DefaultTune:      s.DefaultTune,
		DefaultCharacter: s.DefaultCharacter,
		NixCacheURL:      s.NixCacheURL,
		Chest:            ChestConfig{Backend: s.Chest.Backend},
	}
}

// ConfigShowHandler returns the current server config as JSON.
func (d *Deps) ConfigShowHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p struct{}
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	srv, err := d.loadServerConfig()
	if err != nil {
		return nil, err
	}
	return toServerConfig(srv), nil
}

// ConfigSetHandler updates a single dotted key in the server config and
// atomically rewrites config.yaml. Unknown keys yield `code:2 invalid_flag`.
func (d *Deps) ConfigSetHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p ConfigSetParams
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	if p.Key == "" {
		return nil, rpcerr.UserError(rpcerr.TypeInvalidFlag, "config.set: key is required")
	}
	srv, err := d.loadServerConfig()
	if err != nil {
		return nil, err
	}
	if err := applyConfigKey(srv, p.Key, p.Value); err != nil {
		return nil, err
	}
	if err := config.SaveServer(d.serverConfigPath(), srv); err != nil {
		return nil, rpcerr.Internal("config.set: %v", err).Wrap(err)
	}
	return toServerConfig(srv), nil
}

// applyConfigKey mutates s so the dotted key resolves to value. The set of
// recognized keys mirrors plans/PLAN.md § Server state layout; anything else
// is a user error.
func applyConfigKey(s *config.Server, key, value string) error {
	switch key {
	case "default_tune":
		s.DefaultTune = value
	case "default_character":
		s.DefaultCharacter = value
	case "nix_cache_url":
		s.NixCacheURL = value
	case "chest.backend":
		s.Chest.Backend = value
	default:
		return rpcerr.UserError(rpcerr.TypeInvalidFlag,
			"config.set: unknown key %q", key).With("key", key)
	}
	if err := s.Validate(); err != nil {
		return rpcerr.UserError(rpcerr.TypeInvalidFlag,
			"config.set: %v", err).With("key", key)
	}
	return nil
}

func (d *Deps) loadServerConfig() (*config.Server, error) {
	srv, err := config.LoadServer(d.serverConfigPath())
	if err != nil {
		// LoadServer is strict — a missing config means init hasn't been
		// run yet. Surface that as an internal error so the CLI prints a
		// useful hint rather than a silent empty payload.
		return nil, rpcerr.Internal("config: %v", err).Wrap(err)
	}
	return srv, nil
}

// serverConfigPath resolves the garage-side config.yaml path honoring the
// Deps.GarageDir override first, then falling back to [config.ServerConfigPath].
// The fallback error is swallowed: a missing $HOME on a circuit is catastrophic
// enough that LoadServer will surface it via the empty-string path.
func (d *Deps) serverConfigPath() string {
	if d.GarageDir != "" {
		return filepath.Join(d.GarageDir, "config.yaml")
	}
	p, _ := config.ServerConfigPath()
	return p
}
