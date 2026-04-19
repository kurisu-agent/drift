package drift

import (
	"context"
	"encoding/json"

	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/wire"
)

type startCmd struct {
	Name string `arg:"" help:"Kart name."`
}

func runKartStart(ctx context.Context, io IO, root *CLI, cmd startCmd, deps deps) int {
	return runKartLifecycle(ctx, io, root, cmd.Name, wire.MethodKartStart, "started", deps)
}

// runKartLifecycle handles start/stop/restart/delete — they differ only
// by method name and the stdout verb fragment. delete's not_found comes
// back as a structured rpcerr so the shared path doesn't special-case it.
func runKartLifecycle(ctx context.Context, io IO, root *CLI, name, method, verb string, deps deps) int {
	circuit, err := resolveCircuit(root, deps)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	var raw json.RawMessage
	if err := deps.call(ctx, circuit, method, map[string]string{"name": name}, &raw); err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	return emitKartResult(io, root, verb, raw)
}
