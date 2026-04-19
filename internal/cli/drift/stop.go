package drift

import (
	"context"

	"github.com/kurisu-agent/drift/internal/wire"
)

type stopCmd struct {
	Name string `arg:"" help:"Kart name."`
}

func runKartStop(ctx context.Context, io IO, root *CLI, cmd stopCmd, deps deps) int {
	return runKartLifecycle(ctx, io, root, cmd.Name, wire.MethodKartStop, "stopping", "stopped", deps)
}
