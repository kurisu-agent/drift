package drift

import (
	"context"

	"github.com/kurisu-agent/drift/internal/config"
	"github.com/kurisu-agent/drift/internal/rpc/client"
)

// deps bundles the external dependencies the drift CLI reaches into. Tests
// replace the probe hook and config-path resolver with fakes so no real SSH
// is invoked.
type deps struct {
	// clientConfigPath returns the absolute path to ~/.config/drift/config.yaml,
	// honoring XDG_CONFIG_HOME. Separated so tests can point it at a tempdir.
	clientConfigPath func() (string, error)

	// probe runs a server.version RPC against the given circuit. Returning
	// (nil, nil) is not legal; callers always produce either a non-nil result
	// on success or an error on failure.
	probe func(ctx context.Context, circuit string) (*probeResult, error)
}

// defaultDeps wires the production implementations.
func defaultDeps() deps {
	return deps{
		clientConfigPath: config.ClientConfigPath,
		probe:            defaultProbe(client.New()),
	}
}
