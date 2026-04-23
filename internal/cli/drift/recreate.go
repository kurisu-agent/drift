package drift

import (
	"context"

	"github.com/kurisu-agent/drift/internal/wire"
)

type recreateCmd struct {
	Name string `arg:"" help:"Kart name."`
}

func runKartRecreate(ctx context.Context, io IO, root *CLI, cmd recreateCmd, deps deps) int {
	return runKartLifecycle(ctx, io, root, cmd.Name, wire.MethodKartRecreate, "recreating", "recreated", deps)
}
