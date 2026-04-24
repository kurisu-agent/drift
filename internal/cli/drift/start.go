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
	Name string `arg:"" optional:"" help:"Kart name; omit on a TTY to pick from a cross-circuit kart list."`
}

func runKartStart(ctx context.Context, io IO, root *CLI, cmd startCmd, deps deps) int {
	circuit, name, ok, rc := resolveKartTarget(ctx, io, root, deps, cmd.Name, "drift start")
	if !ok {
		return rc
	}
	return runKartLifecycleOn(ctx, io, root, circuit, name, wire.MethodKartStart, "starting", "started", deps)
}

// runKartLifecycle handles start/stop/restart/delete — they differ only
// by method name and the stdout verb fragment. delete's not_found comes
// back as a structured rpcerr so the shared path doesn't special-case it.
// Used by verbs that don't support the picker (restart/recreate/rebuild);
// the picker-capable verbs resolve their own (circuit, name) via
// resolveKartTarget and call runKartLifecycleOn directly.
func runKartLifecycle(ctx context.Context, io IO, root *CLI, name, method, activeVerb, pastVerb string, deps deps) int {
	_, circuit, err := resolveCircuit(root, deps)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	return runKartLifecycleOn(ctx, io, root, circuit, name, method, activeVerb, pastVerb, deps)
}

// runKartLifecycleOn runs the lifecycle RPC against a pre-resolved
// (circuit, name). The picker paths in start/stop/delete go through here
// so the user's choice from the cross-circuit picker is honored without
// re-resolving the default circuit.
func runKartLifecycleOn(ctx context.Context, io IO, root *CLI, circuit, name, method, activeVerb, pastVerb string, deps deps) int {
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
