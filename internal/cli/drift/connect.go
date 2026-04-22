package drift

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"

	"github.com/charmbracelet/huh"
	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/cli/progress"
	"github.com/kurisu-agent/drift/internal/cli/style"
	"github.com/kurisu-agent/drift/internal/connect"
	driftexec "github.com/kurisu-agent/drift/internal/exec"
	"github.com/kurisu-agent/drift/internal/wire"
)

type connectCmd struct {
	Name         string `arg:"" optional:"" help:"Kart name; omit on a TTY to pick from a list."`
	SSH          bool   `name:"ssh" help:"Force plain SSH (skip mosh)."`
	ForwardAgent bool   `name:"forward-agent" help:"Enable SSH agent forwarding (-A)."`
}

func runConnect(ctx context.Context, io IO, root *CLI, cmd connectCmd, deps deps) int {
	_, circuit, err := resolveCircuit(root, deps)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	name := cmd.Name
	if name == "" {
		// No name + interactive terminal + text output: let the user pick
		// from kart.list. Scripted / JSON callers must pass a name
		// explicitly — we error out rather than hang on a prompt they
		// can't answer.
		if !stdinIsTTY(io.Stdin) || !stdoutIsTTY(io.Stdout) || root.Output == "json" {
			return errfmt.Emit(io.Stderr,
				errors.New("drift connect requires a kart name (non-interactive)"))
		}
		picked, ok, pErr := pickConnectKart(ctx, io, deps, circuit)
		if pErr != nil {
			return errfmt.Emit(io.Stderr, pErr)
		}
		if !ok {
			return 0
		}
		name = picked
	}
	return doConnect(ctx, io, root, deps, circuit, name, cmd.SSH, cmd.ForwardAgent)
}

// pickConnectKart fetches kart.list and renders a huh.Select over the
// result. Returns (name, true, nil) on selection, (_, false, nil) on
// abort / empty list (the latter prints its own notice), err on fatal
// RPC or picker failure.
func pickConnectKart(ctx context.Context, io IO, deps deps, circuit string) (string, bool, error) {
	var raw json.RawMessage
	if err := deps.call(ctx, circuit, wire.MethodKartList, struct{}{}, &raw); err != nil {
		return "", false, err
	}
	var res listResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", false, err
	}
	if len(res.Karts) == 0 {
		fmt.Fprintln(io.Stderr, "no karts on this circuit")
		return "", false, nil
	}
	opts := make([]huh.Option[string], 0, len(res.Karts))
	for _, k := range res.Karts {
		status := k.Status
		if k.Stale {
			status += " (stale)"
		}
		src := k.Source.Mode
		if k.Source.URL != "" {
			// Redact embedded creds in case the kart was cloned from an
			// https://<pat>@host URL — same reasoning as migrate.go.
			src = k.Source.Mode + " " + driftexec.RedactSecrets(k.Source.URL)
		}
		label := fmt.Sprintf("%-24s  %-18s  %s", k.Name, "("+status+")", src)
		opts = append(opts, huh.NewOption(label, k.Name))
	}
	var pick string
	sel := huh.NewSelect[string]().
		Title("drift connect — pick a kart").
		Description("type to filter · enter to connect · esc to cancel").
		Options(opts...).
		Filtering(true).
		Height(18).
		Value(&pick)
	if err := huh.NewForm(huh.NewGroup(sel)).Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return "", false, nil
		}
		return "", false, err
	}
	return pick, true, nil
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
