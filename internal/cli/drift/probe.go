package drift

import (
	"context"
	"time"

	"github.com/kurisu-agent/drift/internal/rpc/client"
	"github.com/kurisu-agent/drift/internal/wire"
)

type probeResult struct {
	Version   string        `json:"version"`
	API       int           `json:"api"`
	Latency   time.Duration `json:"-"`
	LatencyMS int64         `json:"latency_ms"`
}

type versionResult struct {
	Version string `json:"version"`
	API     int    `json:"api"`
}

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
