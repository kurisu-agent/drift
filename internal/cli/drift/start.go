package drift

import (
	"context"
	"encoding/json"

	"github.com/kurisu-agent/drift/internal/wire"
)

// startCmd is `drift start <kart>`.
type startCmd struct {
	Name string `arg:"" help:"Kart name."`
}

func runKartStart(ctx context.Context, io IO, root *CLI, cmd startCmd, deps deps) int {
	return runKartLifecycle(ctx, io, root, cmd.Name, wire.MethodKartStart, "started", deps)
}

// runKartLifecycle is the shared client-side path for start/stop/restart/
// delete. It lives alongside start because start is the canonical caller;
// the other verbs are thin wrappers that only differ by method name and the
// verb fragment used in the stdout summary. delete surfaces not_found from
// the server as a structured rpcerr, so the shared path handles it without
// special-casing.
func runKartLifecycle(ctx context.Context, io IO, root *CLI, name, method, verb string, deps deps) int {
	circuit, err := resolveCircuit(root, deps)
	if err != nil {
		return emitError(io, err)
	}
	var raw json.RawMessage
	if err := deps.call(ctx, circuit, method, map[string]string{"name": name}, &raw); err != nil {
		return emitError(io, err)
	}
	return emitKartResult(io, root, verb, raw)
}
