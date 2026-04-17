package drift

import (
	"context"

	"github.com/kurisu-agent/drift/internal/wire"
)

// disableCmd is `drift disable <kart>` — turns off systemd autostart.
type disableCmd struct {
	Name string `arg:"" help:"Kart name."`
}

func runKartDisable(ctx context.Context, io IO, root *CLI, cmd disableCmd, deps deps) int {
	return runKartAutostart(ctx, io, root, cmd.Name, wire.MethodKartDisable, "disabled", deps)
}
