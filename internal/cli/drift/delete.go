package drift

import (
	"context"

	"github.com/kurisu-agent/drift/internal/wire"
)

// deleteCmd errors on missing (unlike start/stop/restart); not_found
// flows through errfmt.Emit like any other rpcerr.
type deleteCmd struct {
	Name string `arg:"" help:"Kart name."`
}

func runKartDelete(ctx context.Context, io IO, root *CLI, cmd deleteCmd, deps deps) int {
	return runKartLifecycle(ctx, io, root, cmd.Name, wire.MethodKartDelete, "deleted", deps)
}
