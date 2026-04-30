package drift

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/wire"
)

type enableCmd struct {
	Name string `arg:"" help:"Kart name."`
}

func runKartEnable(ctx context.Context, io IO, root *CLI, cmd enableCmd, deps deps) int {
	return runKartAutostart(ctx, io, root, cmd.Name, wire.MethodKartEnable, "enabled", deps)
}

// runKartAutostart: enable/disable are idempotent — redundant calls still
// return 0 with the final state.
func runKartAutostart(ctx context.Context, io IO, root *CLI, name, method, verb string, deps deps) int {
	_, circuit, err := resolveCircuit(root, deps)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	var raw json.RawMessage
	if err := deps.call(ctx, circuit, method, map[string]string{"name": name}, &raw); err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	return emitAutostartResult(io, root, verb, raw)
}

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
		return errfmt.Emit(io.Stderr, err)
	}
	fmt.Fprintf(io.Stdout, "%s autostart for kart %q (enabled=%t)\n", verb, res.Name, res.Enabled)
	return 0
}
