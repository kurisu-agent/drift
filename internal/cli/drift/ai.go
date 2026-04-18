package drift

import (
	"context"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"syscall"
	"time"

	"github.com/kurisu-agent/drift/internal/connect"
)

// aiCmd is `drift ai`. It mosh/ssh's into the target circuit and execs
// `claude --dangerously-skip-permissions` from $HOME/.drift, where
// `lakitu init` has dropped a CLAUDE.md describing drift's commands and
// state layout.
type aiCmd struct {
	SSH          bool `name:"ssh" help:"Force plain SSH (skip mosh)."`
	ForwardAgent bool `name:"forward-agent" help:"Enable SSH agent forwarding (-A)."`
}

// remoteAICmd is the single-line shell snippet handed to mosh/ssh's remote
// shell. We `cd` into ~/.drift so Claude picks up the CLAUDE.md there, then
// exec so the process tree is claude directly (not a wrapper sh).
const remoteAICmd = `cd "$HOME/.drift" && exec claude --dangerously-skip-permissions`

// runAI connects to the circuit host directly (not a kart) and execs claude
// in ~/.drift. Unlike `drift connect`, there's no kart state machine — the
// remote shell is plain lakitu-less ssh into the circuit user's $HOME.
func runAI(ctx context.Context, io IO, root *CLI, cmd aiCmd, deps deps) int {
	circuit, err := resolveCircuit(root, deps)
	if err != nil {
		return emitError(io, err)
	}
	useMosh := !cmd.SSH && moshOnPath()
	bin, argv := buildAIArgv(useMosh, circuit, cmd.ForwardAgent)
	stdio := connect.Stdio{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}

	err = execAI(ctx, bin, argv, stdio)
	if err == nil {
		return 0
	}
	var ee *connect.ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	return emitError(io, err)
}

// buildAIArgv constructs the command to exec. For mosh, remoteAICmd goes
// after `--` so mosh hands it straight to the remote shell; ssh uses `-t`
// to force a pty since claude is interactive.
func buildAIArgv(useMosh bool, circuit string, forwardAgent bool) (string, []string) {
	target := "drift." + circuit
	if useMosh {
		return "mosh", []string{target, "--", "sh", "-c", remoteAICmd}
	}
	args := []string{"-t"}
	if forwardAgent {
		args = append(args, "-A")
	}
	args = append(args, target, remoteAICmd)
	return "ssh", args
}

// moshOnPath returns true iff mosh is available on the client's PATH. We
// don't probe the remote — a client without mosh falls back to ssh even if
// the circuit has mosh-server installed.
func moshOnPath() bool {
	_, err := osexec.LookPath("mosh")
	return err == nil
}

// execAI wires stdio straight through (so claude owns the TTY) and maps a
// non-zero child exit into a connect.ExitError so runAI can pass it up to
// os.Exit without errfmt wrapping.
func execAI(ctx context.Context, bin string, argv []string, stdio connect.Stdio) error {
	c := osexec.CommandContext(ctx, bin, argv...)
	c.Stdin = stdio.Stdin
	c.Stdout = stdio.Stdout
	c.Stderr = stdio.Stderr
	c.Cancel = func() error { return c.Process.Signal(syscall.SIGTERM) }
	c.WaitDelay = 5 * time.Second
	err := c.Run()
	if err == nil {
		return nil
	}
	var ee *osexec.ExitError
	if errors.As(err, &ee) {
		return &connect.ExitError{Code: ee.ExitCode()}
	}
	return fmt.Errorf("exec %s: %w", bin, err)
}
