package drift

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/cli/progress"
	"github.com/kurisu-agent/drift/internal/cli/style"
	"github.com/kurisu-agent/drift/internal/wire"
)

type startCmd struct {
	Name string `arg:"" help:"Kart name."`
}

func runKartStart(ctx context.Context, io IO, root *CLI, cmd startCmd, deps deps) int {
	return runKartLifecycle(ctx, io, root, cmd.Name, wire.MethodKartStart, "starting", "started", deps)
}

// runKartLifecycle handles start/stop/restart/delete — they differ only
// by method name and the stdout verb fragment. delete's not_found comes
// back as a structured rpcerr so the shared path doesn't special-case it.
func runKartLifecycle(ctx context.Context, io IO, root *CLI, name, method, activeVerb, pastVerb string, deps deps) int {
	circuit, err := resolveCircuit(root, deps)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	writeLifecyclePreflight(io.Stderr, root.Output == "json", circuit, method, name)
	// Spinner on stderr; no-op under --output json / non-TTY so scripted
	// usage is unchanged. Suppressed under --debug too, same reason as
	// `drift new`: live server-side output streaming over SSH stderr
	// fights the spinner redraws.
	quiet := root.Output == "json" || root.Debug
	ph := progress.Start(io.Stderr, quiet,
		activeVerb+" kart \""+name+"\"", "ssh")
	var raw json.RawMessage
	if err := deps.call(ctx, circuit, method, map[string]string{"name": name}, &raw); err != nil {
		ph.Fail()
		return errfmt.Emit(io.Stderr, err)
	}
	ph.Succeed(pastVerb + " kart \"" + name + "\"")
	return emitKartResult(io, root, pastVerb, raw)
}

// writeLifecyclePreflight mirrors writeNewPreflight's shape but for
// start/stop/restart/delete: a dim `→ <method> on circuit "X"` block on
// stderr so the user sees what's about to be sent before the (often
// multi-second) RPC blocks. Skipped under --output json so structured
// callers stay clean.
func writeLifecyclePreflight(w interface{ Write(p []byte) (int, error) }, jsonMode bool, circuit, method, name string) {
	if jsonMode {
		return
	}
	p := style.For(w, false)
	fmt.Fprintln(w, p.Dim(fmt.Sprintf("→ %s on circuit %q", method, circuit)))
	fmt.Fprintln(w, p.Dim(fmt.Sprintf("  name: %s", name)))
}
