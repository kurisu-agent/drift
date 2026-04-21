// Package server holds the lakitu-side handler implementations. Handlers
// are pure functions over a [Deps] bundle so both the JSON-RPC path and
// human `lakitu <subcommand>` path can call them.
package server

import (
	"github.com/kurisu-agent/drift/internal/chest"
	"github.com/kurisu-agent/drift/internal/config"
	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/wire"
)

// Deps resolves garage paths and opens the chest backend lazily so
// `server.version` keeps working on a circuit that hasn't run `lakitu init`.
type Deps struct {
	// GarageDir overrides the resolved path. Tests set this to a tempdir.
	GarageDir string
	// DriftHome overrides the $HOME/.drift path. Tests set this so the
	// run-registry handlers read a fixture runs.yaml instead of the real
	// user home. Empty falls back to config.DriftHomeDir().
	DriftHome string
	// OpenChest: nil runs per-call [chest.Open] against the current server
	// config so a backend swap via `config.set` takes effect next RPC.
	OpenChest func(garageDir string, cfg config.ChestConfig) (chest.Backend, error)
}

func RegisterServer(reg *rpc.Registry, d *Deps) {
	if d == nil {
		d = &Deps{}
	}
	reg.Register(wire.MethodServerVersion, VersionHandler)
	reg.Register(wire.MethodServerInfo, d.InfoHandler)
	reg.Register(wire.MethodServerVerify, VerifyHandler)

	reg.Register(wire.MethodConfigShow, d.ConfigShowHandler)
	reg.Register(wire.MethodConfigSet, d.ConfigSetHandler)

	reg.Register(wire.MethodCharacterAdd, d.CharacterAddHandler)
	reg.Register(wire.MethodCharacterList, d.CharacterListHandler)
	reg.Register(wire.MethodCharacterShow, d.CharacterShowHandler)
	reg.Register(wire.MethodCharacterRemove, d.CharacterRemoveHandler)

	reg.Register(wire.MethodTuneList, d.TuneListHandler)
	reg.Register(wire.MethodTuneShow, d.TuneShowHandler)
	reg.Register(wire.MethodTuneSet, d.TuneSetHandler)
	reg.Register(wire.MethodTuneRemove, d.TuneRemoveHandler)

	reg.Register(wire.MethodChestSet, d.ChestSetHandler)
	reg.Register(wire.MethodChestGet, d.ChestGetHandler)
	reg.Register(wire.MethodChestList, d.ChestListHandler)
	reg.Register(wire.MethodChestRemove, d.ChestRemoveHandler)

	reg.Register(wire.MethodRunList, d.RunListHandler)
	reg.Register(wire.MethodRunResolve, d.RunResolveHandler)
}

func (d *Deps) driftHome() (string, error) {
	if d.DriftHome != "" {
		return d.DriftHome, nil
	}
	return config.DriftHomeDir()
}

func (d *Deps) garageDir() (string, error) {
	if d.GarageDir != "" {
		return d.GarageDir, nil
	}
	return config.GarageDir()
}

// OpenChestForLifecycle is the exported adapter kart-lifecycle handlers
// bind to KartDeps.OpenChest so start/restart can re-resolve chest refs
// from the current on-disk backend. Thin wrapper around openChest so the
// lazy-on-demand contract (tolerate a missing garage) is preserved.
func (d *Deps) OpenChestForLifecycle() (chest.Backend, error) { return d.openChest() }

// openChest is called lazily — `server.version` must not require the
// garage to exist.
func (d *Deps) openChest() (chest.Backend, error) {
	garage, err := d.garageDir()
	if err != nil {
		return nil, rpcerr.Internal("resolve garage dir: %v", err).Wrap(err)
	}
	srv, err := config.LoadServer(d.serverConfigPath())
	if err != nil {
		return nil, rpcerr.Internal("config: %v", err).Wrap(err)
	}
	open := d.OpenChest
	if open == nil {
		open = chest.Open
	}
	backend, err := open(garage, srv.Chest)
	if err != nil {
		return nil, rpcerr.Internal("chest: %v", err).Wrap(err)
	}
	return backend, nil
}
