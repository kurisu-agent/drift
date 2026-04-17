package server

import (
	"context"
	"encoding/json"

	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/version"
)

// VersionResult is the shape returned by `server.version`. Mirrors
// plans/PLAN.md § Version compatibility.
type VersionResult struct {
	Version string `json:"version"`
	API     int    `json:"api"`
}

// Version returns the current lakitu version + API schema. Shared between
// the RPC handler and the `lakitu version` subcommand so both surfaces emit
// the same payload.
func Version() VersionResult {
	info := version.Get()
	return VersionResult{Version: info.Version, API: info.APISchema}
}

// VersionHandler dispatches the server.version RPC. Takes no params — a
// strict bind ensures the client hasn't smuggled unexpected fields.
func VersionHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p struct{}
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	return Version(), nil
}
