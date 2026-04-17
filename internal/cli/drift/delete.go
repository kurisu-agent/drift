package drift

import (
	"context"

	"github.com/kurisu-agent/drift/internal/wire"
)

// deleteCmd is `drift delete <kart>`. Unlike start/stop/restart, delete
// errors on missing per plans/PLAN.md § Idempotency; the not_found surface
// is produced by the server and flows through emitError like any other
// rpcerr.
type deleteCmd struct {
	Name string `arg:"" help:"Kart name."`
}

func runKartDelete(ctx context.Context, io IO, root *CLI, cmd deleteCmd, deps deps) int {
	return runKartLifecycle(ctx, io, root, cmd.Name, wire.MethodKartDelete, "deleted", deps)
}
