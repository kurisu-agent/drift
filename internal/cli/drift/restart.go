package drift

import (
	"context"

	"github.com/kurisu-agent/drift/internal/wire"
)

// restartCmd is `drift restart <kart>`.
type restartCmd struct {
	Name string `arg:"" help:"Kart name."`
}

func runKartRestart(ctx context.Context, io IO, root *CLI, cmd restartCmd, deps deps) int {
	return runKartLifecycle(ctx, io, root, cmd.Name, wire.MethodKartRestart, "restarted", deps)
}
