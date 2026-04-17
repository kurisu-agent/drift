package drift

import (
	"context"

	"github.com/kurisu-agent/drift/internal/wire"
)

// stopCmd is `drift stop <kart>`.
type stopCmd struct {
	Name string `arg:"" help:"Kart name."`
}

func runKartStop(ctx context.Context, io IO, root *CLI, cmd stopCmd, deps deps) int {
	return runKartLifecycle(ctx, io, root, cmd.Name, wire.MethodKartStop, "stopped", deps)
}
