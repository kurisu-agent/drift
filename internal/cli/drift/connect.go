package drift

import (
	"context"
	"errors"
	"os"

	"github.com/kurisu-agent/drift/internal/connect"
	"github.com/kurisu-agent/drift/internal/rpc/client"
)

// connectCmd is `drift connect <kart>`.
type connectCmd struct {
	Name         string `arg:"" help:"Kart name."`
	SSH          bool   `name:"ssh" help:"Force plain SSH (skip mosh)."`
	ForwardAgent bool   `name:"forward-agent" help:"Enable SSH agent forwarding (-A)."`
}

// runConnect dispatches to the internal/connect state machine. stdin/stdout/
// stderr are wired straight through so the child (mosh or ssh) owns the TTY.
// drift's own logs go to stderr; the child's stderr is interleaved.
func runConnect(ctx context.Context, io IO, root *CLI, cmd connectCmd, deps deps) int {
	circuit, err := resolveCircuit(root, deps)
	if err != nil {
		return emitError(io, err)
	}
	rpcClient := client.New()
	d := connect.Deps{
		Call: rpcClient.Call,
	}
	opts := connect.Options{
		Circuit:      circuit,
		Kart:         cmd.Name,
		ForceSSH:     cmd.SSH,
		ForwardAgent: cmd.ForwardAgent,
	}
	stdio := connect.Stdio{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}

	err = connect.Run(ctx, d, opts, stdio)
	if err == nil {
		return 0
	}
	// Pass through the remote session's exit code verbatim — we don't want
	// a non-zero exit from the user's own shell to be wrapped in errfmt's
	// "error:" prefix, so branch on ExitError here before emitError.
	var ee *connect.ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	return emitError(io, err)
}
