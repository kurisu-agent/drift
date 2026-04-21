package server

import (
	"context"
	"encoding/json"
	"path/filepath"

	"github.com/kurisu-agent/drift/internal/config"
	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/rpcerr"
)

type ConfigSetParams struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// ServerConfig is the JSON mirror of config.Server (yaml on disk, snake_case
// JSON on the wire).
type ServerConfig struct {
	Name             string      `json:"name,omitempty"`
	DefaultTune      string      `json:"default_tune"`
	DefaultCharacter string      `json:"default_character"`
	NixCacheURL      string      `json:"nix_cache_url"`
	Chest            ChestConfig `json:"chest"`
}

type ChestConfig struct {
	Backend string `json:"backend"`
}

func toServerConfig(s *config.Server) ServerConfig {
	return ServerConfig{
		Name:             s.ResolveName(),
		DefaultTune:      s.DefaultTune,
		DefaultCharacter: s.DefaultCharacter,
		NixCacheURL:      s.NixCacheURL,
		Chest:            ChestConfig{Backend: s.Chest.Backend},
	}
}

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

// ConfigSetHandler rewrites config.yaml atomically. Unknown keys yield
// code:2 invalid_flag.
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

func applyConfigKey(s *config.Server, key, value string) error {
	switch key {
	case "name":
		s.Name = value
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

// LoadServerConfig loads the server config.yaml via the Deps-resolved
// garage. Exported so embedding/wrapping handlers (e.g. KartMigrateDeps)
// don't have to duplicate the load path.
func (d *Deps) LoadServerConfig() (*config.Server, error) { return d.loadServerConfig() }

func (d *Deps) loadServerConfig() (*config.Server, error) {
	srv, err := config.LoadServer(d.serverConfigPath())
	if err != nil {
		// LoadServer is strict — a missing config means init hasn't been
		// run. Surface as internal so the CLI prints a useful hint.
		return nil, rpcerr.Internal("config: %v", err).Wrap(err)
	}
	return srv, nil
}

// ServerConfigPath is the exported form of serverConfigPath — see the
// private version for the $HOME fallback contract.
func (d *Deps) ServerConfigPath() string { return d.serverConfigPath() }

// serverConfigPath swallows the fallback error: a missing $HOME on a
// circuit is catastrophic enough that LoadServer will surface it via the
// empty-string path anyway.
func (d *Deps) serverConfigPath() string {
	if d.GarageDir != "" {
		return filepath.Join(d.GarageDir, "config.yaml")
	}
	p, _ := config.ServerConfigPath()
	return p
}
