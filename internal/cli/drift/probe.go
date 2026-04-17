package drift

import (
	"context"
	"time"

	"github.com/kurisu-agent/drift/internal/rpc/client"
	"github.com/kurisu-agent/drift/internal/wire"
)

// probeResult holds what a successful server.version probe returns to the CLI
// layer for display.
type probeResult struct {
	Version string        `json:"version"`
	API     int           `json:"api"`
	Latency time.Duration `json:"-"`
	// LatencyMS mirrors Latency as integer milliseconds for JSON output.
	LatencyMS int64 `json:"latency_ms"`
}

// versionResult mirrors the JSON shape of server.version — see
// plans/PLAN.md § Version compatibility.
type versionResult struct {
	Version string `json:"version"`
	API     int    `json:"api"`
}

// defaultProbe returns a probe function that issues a server.version RPC via
// the supplied client. The function measures wall-clock round-trip time so
// the caller can surface it to users.
func defaultProbe(c *client.Client) func(ctx context.Context, circuit string) (*probeResult, error) {
	return func(ctx context.Context, circuit string) (*probeResult, error) {
		var vr versionResult
		start := time.Now()
		if err := c.Call(ctx, circuit, wire.MethodServerVersion, nil, &vr); err != nil {
			return nil, err
		}
		elapsed := time.Since(start)
		return &probeResult{
			Version:   vr.Version,
			API:       vr.API,
			Latency:   elapsed,
			LatencyMS: elapsed.Milliseconds(),
		}, nil
	}
}
