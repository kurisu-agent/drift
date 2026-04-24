package drift

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/cli/style"
	"github.com/kurisu-agent/drift/internal/connect"
	driftexec "github.com/kurisu-agent/drift/internal/exec"
	"github.com/kurisu-agent/drift/internal/wire"
)

// aiCmd: `drift ai` — bare claude REPL on the circuit. Direct replacement
// for the old `drift run ai` shorthand. Intentionally unparameterised:
// skill-flavoured invocations live under `drift skill` so the two verbs
// stay single-purpose.
type aiCmd struct {
	SSH          bool `name:"ssh" help:"Force plain SSH (skip mosh)."`
	ForwardAgent bool `name:"forward-agent" help:"Enable SSH agent forwarding (-A)."`
}

// bareClaudeCommand is the remote shell snippet `drift ai` hands to
// ssh/mosh. Kept client-side — no RPC round-trip is needed because the
// command is fixed and the server has no say in the shape.
//
// When zellij is available on the circuit and we're not already inside
// one, wrap claude in a fresh zellij session so the user gets
// scrollback / panes / keybinds — same UX they get from an interactive
// login to the circuit. Without the wrap, `sh -c claude` runs straight
// on the mosh/ssh PTY and the user loses scroll-back the moment claude's
// TUI redraws. The layout is materialized to a tempfile because zellij's
// --layout-string rejects single-line KDL (it wants real newlines,
// awkward to embed cross-shell); writing a file sidesteps that.
const bareClaudeCommand = `cd "$HOME/.drift"
if command -v zellij >/dev/null 2>&1 && [ -z "$ZELLIJ" ]; then
  _lf=$(mktemp --suffix=.kdl)
  cat > "$_lf" <<'ZLAYOUT'
layout {
    pane command="claude" {
        args "--dangerously-skip-permissions"
    }
}
ZLAYOUT
  exec zellij --layout "$_lf"
fi
exec claude --dangerously-skip-permissions`

func runAIExec(ctx context.Context, io IO, root *CLI, cmd aiCmd, deps deps) int {
	_, circuit, err := resolveCircuit(root, deps)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}

	useMosh := !cmd.SSH && moshOnPath()
	bin, argv := buildRunArgv(wire.RunModeInteractive, useMosh, circuit, cmd.ForwardAgent, bareClaudeCommand)

	p := style.For(io.Stderr, root.Output == "json")
	if p.Enabled {
		transport := "ssh"
		if useMosh {
			transport = "mosh"
		}
		fmt.Fprintln(io.Stderr, p.Dim(fmt.Sprintf("→ ai (interactive, via %s)", transport)))
	}

	stdio := connect.Stdio{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}
	if execErr := driftexec.Interactive(ctx, bin, argv, stdio.Stdin, stdio.Stdout, stdio.Stderr); execErr != nil {
		var ee *driftexec.Error
		if errors.As(execErr, &ee) && ee.ExitCode != 0 {
			return ee.ExitCode
		}
		return errfmt.Emit(io.Stderr, execErr)
	}
	return 0
}
