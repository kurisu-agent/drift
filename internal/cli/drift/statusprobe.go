package drift

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/kurisu-agent/drift/internal/rpc/client"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/wire"
)

// statusProbeResult merges the server.version probe payload with the
// kart.list payload so `drift status` can render its full output from a
// single SSH round-trip per circuit. Karts is left as a raw JSON array
// — callers decode it through their own listEntry type so additive
// server fields ride through unchanged.
type statusProbeResult struct {
	Version   string
	API       int
	LatencyMS int64
	Karts     json.RawMessage
}

// defaultStatusProbe returns the probe that `drift status` uses on the
// hot path. It calls server.status (one round-trip), and on method_not_
// found falls back to server.version + kart.list — older lakitus that
// don't ship server.status keep working at the cost of two RPCs.
//
// fallbackProbe and fallbackList are decoupled rather than reusing
// deps.probe/deps.call so unit tests can fake the fallback path
// independently of the primary path.
func defaultStatusProbe(c *client.Client) func(ctx context.Context, circuit string) (*statusProbeResult, error) {
	probe := defaultProbe(c)
	return func(ctx context.Context, circuit string) (*statusProbeResult, error) {
		var combined struct {
			Version string          `json:"version"`
			API     int             `json:"api"`
			Karts   json.RawMessage `json:"karts"`
		}
		start := time.Now()
		err := c.Call(ctx, circuit, wire.MethodServerStatus, struct{}{}, &combined)
		elapsed := time.Since(start)
		if err == nil {
			return &statusProbeResult{
				Version:   combined.Version,
				API:       combined.API,
				LatencyMS: elapsed.Milliseconds(),
				Karts:     combined.Karts,
			}, nil
		}
		var re *rpcerr.Error
		if !errors.As(err, &re) || re.Type != "method_not_found" {
			return nil, err
		}
		// Old lakitu: replay the historical two-call sequence so the
		// command still works without a coordinated upgrade.
		pr, perr := probe(ctx, circuit)
		if perr != nil {
			return nil, perr
		}
		var listResult struct {
			Karts json.RawMessage `json:"karts"`
		}
		if err := c.Call(ctx, circuit, wire.MethodKartList, struct{}{}, &listResult); err != nil {
			return nil, err
		}
		return &statusProbeResult{
			Version:   pr.Version,
			API:       pr.API,
			LatencyMS: pr.LatencyMS,
			Karts:     listResult.Karts,
		}, nil
	}
}
