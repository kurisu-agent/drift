package drift

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/kurisu-agent/drift/internal/wire"
)

// enableCmd is `drift enable <kart>` — turns on systemd autostart for a kart.
type enableCmd struct {
	Name string `arg:"" help:"Kart name."`
}

func runKartEnable(ctx context.Context, io IO, root *CLI, cmd enableCmd, deps deps) int {
	return runKartAutostart(ctx, io, root, cmd.Name, wire.MethodKartEnable, "enabled", deps)
}

// runKartAutostart is the shared client-side path for enable/disable. Both
// verbs are idempotent on the server, so a redundant call (already enabled /
// already disabled) still returns 0 with the final state.
func runKartAutostart(ctx context.Context, io IO, root *CLI, name, method, verb string, deps deps) int {
	circuit, err := resolveCircuit(root, deps)
	if err != nil {
		return emitError(io, err)
	}
	var raw json.RawMessage
	if err := deps.call(ctx, circuit, method, map[string]string{"name": name}, &raw); err != nil {
		return emitError(io, err)
	}
	return emitAutostartResult(io, root, verb, raw)
}

// emitAutostartResult renders the kart.enable/disable response. JSON passes
// through verbatim; text output is a single line summarizing the final state.
func emitAutostartResult(io IO, root *CLI, verb string, raw json.RawMessage) int {
	if root != nil && root.Output == "json" {
		fmt.Fprintln(io.Stdout, string(raw))
		return 0
	}
	var res struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return emitError(io, err)
	}
	fmt.Fprintf(io.Stdout, "%s autostart for kart %q (enabled=%t)\n", verb, res.Name, res.Enabled)
	return 0
}
