package drift

import (
	"context"

	"github.com/kurisu-agent/drift/internal/wire"
)

// rebuildCmd is the user-facing verb for kart.rebuild: re-applies
// the current tune's env + mount_dirs to the kart's captured config
// and runs `devpod up --recreate` so container-shape changes take
// effect. Blows away in-container state.
type rebuildCmd struct {
	Name string `arg:"" help:"Kart name."`
}

func runKartRebuild(ctx context.Context, io IO, root *CLI, cmd rebuildCmd, deps deps) int {
	return runKartLifecycle(ctx, io, root, cmd.Name, wire.MethodKartRebuild, "rebuilding", "rebuilt", deps)
}
