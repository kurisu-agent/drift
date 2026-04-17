package drift

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/kurisu-agent/drift/internal/wire"
)

// logsCmd is `drift logs <kart>`. The MVP surface returns a single chunk of
// output; streaming is deferred to a later phase per plans/PLAN.md § Future.
type logsCmd struct {
	Name string `arg:"" help:"Kart name."`
}

func runKartLogs(ctx context.Context, io IO, root *CLI, cmd logsCmd, deps deps) int {
	circuit, err := resolveCircuit(root, deps)
	if err != nil {
		return emitError(io, err)
	}
	var raw json.RawMessage
	if err := deps.call(ctx, circuit, wire.MethodKartLogs, map[string]string{"name": cmd.Name}, &raw); err != nil {
		return emitError(io, err)
	}
	if root != nil && root.Output == "json" {
		fmt.Fprintln(io.Stdout, string(raw))
		return 0
	}
	var res struct {
		Chunk string `json:"chunk"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return emitError(io, err)
	}
	// Write the raw chunk unmodified — callers may want to pipe or grep it.
	fmt.Fprint(io.Stdout, res.Chunk)
	return 0
}
