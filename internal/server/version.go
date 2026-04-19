package server

import (
	"context"
	"encoding/json"

	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/version"
)

type VersionResult struct {
	Version string `json:"version"`
	API     int    `json:"api"`
}

// Version is shared between the RPC handler and `lakitu version` so both
// surfaces emit the same payload.
func Version() VersionResult {
	info := version.Get()
	return VersionResult{Version: info.Version, API: info.APISchema}
}

func VersionHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p struct{}
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	return Version(), nil
}
