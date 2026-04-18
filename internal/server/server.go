// Package server holds the lakitu-side handler implementations for every
// RPC method except the devpod-backed kart lifecycle (owned by Phase 7).
// Handlers are pure functions over a [Deps] bundle so both the JSON-RPC
// dispatch path and the human `lakitu <subcommand>` path can call them.
//
// Deps resolves garage paths and opens the chest backend at call time so
// `server.version` keeps working on a circuit that hasn't yet run
// `lakitu init`.
package server

import (
	"github.com/kurisu-agent/drift/internal/chest"
	"github.com/kurisu-agent/drift/internal/config"
	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/wire"
)

// Deps bundles everything the handlers in this package need. The fields are
// resolved lazily — a zero Deps is usable and defers every filesystem probe
// to the moment a handler actually needs the garage.
type Deps struct {
	// GarageDir overrides the resolved garage path. Tests set this to a
	// tempdir; production leaves it empty and relies on [config.GarageDir].
	GarageDir string
	// OpenChest overrides the chest backend factory. When nil, a per-call
	// [chest.Open] runs against the current server config so a backend
	// swap via `config.set` takes effect on the next RPC.
	OpenChest func(garageDir string, cfg config.ChestConfig) (chest.Backend, error)
}

// RegisterServer wires every Phase 6 handler into reg. server.init stays in
// cli/lakitu so the existing wiring is untouched; RegisterServer handles
// everything else the phase owns.
func RegisterServer(reg *rpc.Registry, d *Deps) {
	if d == nil {
		d = &Deps{}
	}
	reg.Register(wire.MethodServerVersion, VersionHandler)
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
}

// garageDir returns the effective garage root. Overrides win so test setup
// stays a single struct-literal away.
func (d *Deps) garageDir() (string, error) {
	if d.GarageDir != "" {
		return d.GarageDir, nil
	}
	return config.GarageDir()
}

// openChest loads the server config, then opens the active chest backend.
// Called lazily — `server.version` must not need the garage to exist.
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
