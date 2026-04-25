package server

import (
	"context"
	"encoding/json"

	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/wire"
)

// ServerStatusResult is the wire shape of `server.status`: server.version
// payload + the kart roster, returned in a single round-trip so
// `drift status` doesn't pay two SSH handshakes per circuit.
type ServerStatusResult struct {
	Version string     `json:"version"`
	API     int        `json:"api"`
	Karts   []KartInfo `json:"karts"`
}

// RegisterServerStatus wires the combined server.status handler. Lives
// alongside the kart handlers because it needs KartDeps to fan in
// container state — registering it from RegisterServer (which has no
// devpod/docker wiring) would force a second adapter.
func RegisterServerStatus(reg *rpc.Registry, kartDeps KartDeps) {
	reg.Register(wire.MethodServerStatus, kartDeps.serverStatusHandler)
}

func (d KartDeps) serverStatusHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p struct{}
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	karts, err := d.BuildKartList(ctx)
	if err != nil {
		return nil, err
	}
	v := Version()
	return ServerStatusResult{
		Version: v.Version,
		API:     v.API,
		Karts:   karts,
	}, nil
}
