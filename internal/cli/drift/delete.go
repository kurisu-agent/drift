package drift

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/cli/style"
	"github.com/kurisu-agent/drift/internal/wire"
)

// deleteCmd errors on missing (unlike start/stop/restart); not_found
// flows through errfmt.Emit like any other rpcerr. Destructive, so it
// prompts on a TTY by default — pass -y to skip, which is the only way
// to run this in scripted / non-TTY contexts.
type deleteCmd struct {
	Name  string `arg:"" optional:"" help:"Kart name; omit on a TTY to pick from a cross-circuit kart list."`
	Force bool   `short:"y" name:"yes" aliases:"force" help:"Skip the interactive confirmation prompt."`
}

func runKartDelete(ctx context.Context, io IO, root *CLI, cmd deleteCmd, deps deps) int {
	circuit, name, ok, rc := resolveKartTarget(ctx, io, root, deps, cmd.Name, "drift delete")
	if !ok {
		return rc
	}
	if !cmd.Force {
		confirmed, err := confirmDelete(io, circuit, name)
		if err != nil {
			return errfmt.Emit(io.Stderr, err)
		}
		if !confirmed {
			fmt.Fprintln(io.Stderr, "aborted")
			return 1
		}
	}
	return runKartLifecycleOn(ctx, io, root, circuit, name, wire.MethodKartDelete, "deleting", "deleted", deps)
}

// confirmDelete returns (answer, err). Non-TTY stdin with no -y is a user
// error — silently aborting would hide the problem in CI logs, and auto-
// confirming would be unsafe. Includes the circuit so users coming out
// of the picker see exactly which (circuit, kart) they're about to drop.
func confirmDelete(io IO, circuit, name string) (bool, error) {
	if !stdinIsTTY(io.Stdin) {
		return false, errors.New("drift kart delete requires -y on non-interactive stdin")
	}
	p := style.For(io.Stderr, false)
	fmt.Fprintf(io.Stderr, "%s delete kart %q on circuit %q? [y/N]: ",
		p.Warn("!"), name, circuit)
	br := bufio.NewReader(io.Stdin)
	line, err := br.ReadString('\n')
	if err != nil {
		return false, nil
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	}
	return false, nil
}
