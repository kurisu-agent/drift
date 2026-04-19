package drift

import (
	"context"
	"errors"
	"os"

	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/connect"
	"github.com/kurisu-agent/drift/internal/rpc/client"
)

type connectCmd struct {
	Name         string `arg:"" help:"Kart name."`
	SSH          bool   `name:"ssh" help:"Force plain SSH (skip mosh)."`
	ForwardAgent bool   `name:"forward-agent" help:"Enable SSH agent forwarding (-A)."`
}

func runConnect(ctx context.Context, io IO, root *CLI, cmd connectCmd, deps deps) int {
	circuit, err := resolveCircuit(root, deps)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
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
	// Pass remote exit code through — a non-zero from the user's own
	// shell shouldn't be wrapped in errfmt's "error:" prefix.
	var ee *connect.ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	return errfmt.Emit(io.Stderr, err)
}
