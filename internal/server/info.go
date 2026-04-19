package server

import (
	"context"
	"encoding/json"

	"github.com/kurisu-agent/drift/internal/config"
	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/version"
	"github.com/kurisu-agent/drift/internal/wire"
)

// Info is shared between the RPC handler and `lakitu info` so both surfaces
// emit the same payload. Unlike server.version, server.info reads the
// on-disk config (for Name + DefaultCharacter) — don't call it from
// per-RPC health checks; it's a setup-time probe.
func (d *Deps) Info() wire.ServerInfo {
	v := version.Get()
	out := wire.ServerInfo{
		Version: v.Version,
		API:     v.APISchema,
	}
	// Tolerate a missing config: a circuit that hasn't run `lakitu init`
	// still answers server.info with the hostname-derived Name, which is
	// exactly what `drift init` needs in order to adopt it.
	if srv, err := config.LoadServer(d.serverConfigPath()); err == nil {
		out.Name = srv.ResolveName()
		out.DefaultCharacter = srv.DefaultCharacter
	} else {
		out.Name = (&config.Server{}).ResolveName()
	}
	return out
}

func (d *Deps) InfoHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p struct{}
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	return d.Info(), nil
}
