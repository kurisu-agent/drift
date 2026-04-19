package drift

import (
	"context"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"

	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/cli/style"
	"github.com/kurisu-agent/drift/internal/connect"
	driftexec "github.com/kurisu-agent/drift/internal/exec"
)

// aiCmd mosh/ssh's into the circuit and execs claude from $HOME/.drift,
// where `lakitu init` dropped a CLAUDE.md describing drift's surface.
type aiCmd struct {
	SSH          bool `name:"ssh" help:"Force plain SSH (skip mosh)."`
	ForwardAgent bool `name:"forward-agent" help:"Enable SSH agent forwarding (-A)."`
}

// exec so the process tree is claude directly, not a wrapper sh.
const remoteAICmd = `cd "$HOME/.drift" && exec claude --dangerously-skip-permissions`

// runAI has no kart state machine — it's plain ssh into the circuit user's
// $HOME, unlike `drift connect` which targets a kart.
func runAI(ctx context.Context, io IO, root *CLI, cmd aiCmd, deps deps) int {
	circuit, err := resolveCircuit(root, deps)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	useMosh := !cmd.SSH && moshOnPath()
	bin, argv := buildAIArgv(useMosh, circuit, cmd.ForwardAgent)
	stdio := connect.Stdio{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}

	p := style.For(io.Stderr, root.Output == "json")
	if p.Enabled {
		transport := "ssh"
		if useMosh {
			transport = "mosh"
		}
		fmt.Fprintln(io.Stderr, p.Dim("via "+transport))
	}

	err = execAI(ctx, bin, argv, stdio)
	if err == nil {
		return 0
	}
	var ee *connect.ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	return errfmt.Emit(io.Stderr, err)
}

// buildAIArgv: ssh uses -t to force a pty since claude is interactive.
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

// moshOnPath checks the client PATH only — a client without mosh falls
// back to ssh even when the circuit has mosh-server.
func moshOnPath() bool {
	_, err := osexec.LookPath("mosh")
	return err == nil
}

// execAI routes the interactive claude child through driftexec.Interactive
// and surfaces the child's exit code as connect.ExitError so runAI can
// propagate it without errfmt's "error:" prefix.
func execAI(ctx context.Context, bin string, argv []string, stdio connect.Stdio) error {
	err := driftexec.Interactive(ctx, bin, argv, stdio.Stdin, stdio.Stdout, stdio.Stderr)
	if err == nil {
		return nil
	}
	var ee *driftexec.Error
	if errors.As(err, &ee) {
		return &connect.ExitError{Code: ee.ExitCode}
	}
	return err
}
