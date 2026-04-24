package drift

import (
	"context"

	"github.com/kurisu-agent/drift/internal/wire"
)

type stopCmd struct {
	Name string `arg:"" optional:"" help:"Kart name; omit on a TTY to pick from a cross-circuit kart list."`
}

func runKartStop(ctx context.Context, io IO, root *CLI, cmd stopCmd, deps deps) int {
	circuit, name, ok, rc := resolveKartTarget(ctx, io, root, deps, cmd.Name, "drift stop")
	if !ok {
		return rc
	}
	return runKartLifecycleOn(ctx, io, root, circuit, name, wire.MethodKartStop, "stopping", "stopped", deps)
}
