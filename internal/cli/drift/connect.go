package drift

import (
	"context"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"

	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/cli/progress"
	"github.com/kurisu-agent/drift/internal/cli/style"
	"github.com/kurisu-agent/drift/internal/connect"
)

type connectCmd struct {
	Name         string `arg:"" help:"Kart name."`
	SSH          bool   `name:"ssh" help:"Force plain SSH (skip mosh)."`
	ForwardAgent bool   `name:"forward-agent" help:"Enable SSH agent forwarding (-A)."`
}

func runConnect(ctx context.Context, io IO, root *CLI, cmd connectCmd, deps deps) int {
	_, circuit, err := resolveCircuit(root, deps)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	return doConnect(ctx, io, root, deps, circuit, cmd.Name, cmd.SSH, cmd.ForwardAgent)
}

// doConnect is the shared body behind `drift connect` and the post-create
// auto-connect path of `drift new`. Both paths have already resolved the
// circuit, so the helper takes it as a parameter instead of re-resolving.
func doConnect(ctx context.Context, io IO, root *CLI, deps deps, circuit, name string, forceSSH, forwardAgent bool) int {
	transport := connect.Transport(osexec.LookPath, forceSSH)
	ph := progress.Start(io.Stderr, root.Output == "json",
		"connecting to kart \""+name+"\"", transport)
	d := connect.Deps{
		Call: deps.call,
		// Stop the spinner right before Exec takes the TTY so it doesn't
		// race the interactive child for cursor control.
		OnReady: ph.Stop,
	}
	opts := connect.Options{
		Circuit:      circuit,
		Kart:         name,
		ForceSSH:     forceSSH,
		ForwardAgent: forwardAgent,
	}
	stdio := connect.Stdio{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}

	// Transport hint to stderr so stdout stays clean for the remote
	// session. Silenced in JSON mode / non-TTY via palette gating.
	p := style.For(io.Stderr, root.Output == "json")
	if p.Enabled {
		fmt.Fprintln(io.Stderr, p.Dim("via "+transport))
	}

	err := connect.Run(ctx, d, opts, stdio)
	// If Run returned before reaching Exec (RPC error), the spinner is
	// still running — make sure it cleans up before errfmt writes.
	ph.Stop()
	if err == nil {
		return 0
	}
	// Pass remote exit code through — a non-zero from the user's own
	// shell shouldn't be wrapped in errfmt's "error:" prefix.
	var ee *connect.ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	return errfmt.Emit(io.Stderr, err)
}
