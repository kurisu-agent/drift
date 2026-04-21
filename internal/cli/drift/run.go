package drift

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"strings"

	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/cli/style"
	"github.com/kurisu-agent/drift/internal/connect"
	driftexec "github.com/kurisu-agent/drift/internal/exec"
	"github.com/kurisu-agent/drift/internal/wire"
)

// runsCmd: `drift runs` — list server-side shorthand entries. No args.
// Names, descriptions, and modes come from run.list; command strings are
// deliberately not exposed to the client.
type runsCmd struct{}

// runCmd: `drift run <name> [args…]`. Args are forwarded to the server's
// template expansion. Name is optional so `drift run` alone drops to the
// `drift runs` listing — a nicer affordance than Kong's "expected <name>"
// for users who forget the shorthand.
type runCmd struct {
	Name         string   `arg:"" optional:"" help:"Shorthand name (see drift runs); omit to list."`
	Args         []string `arg:"" optional:"" passthrough:"" help:"Positional args forwarded to the remote command."`
	SSH          bool     `name:"ssh" help:"Force plain SSH (skip mosh) for interactive entries."`
	ForwardAgent bool     `name:"forward-agent" help:"Enable SSH agent forwarding (-A)."`
}

func runRunsList(ctx context.Context, io IO, root *CLI, _ runsCmd, deps deps) int {
	_, circuit, err := resolveCircuit(root, deps)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	var res wire.RunListResult
	if err := deps.call(ctx, circuit, wire.MethodRunList, struct{}{}, &res); err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	if root.Output == "json" {
		buf, mErr := json.MarshalIndent(res, "", "  ")
		if mErr != nil {
			return errfmt.Emit(io.Stderr, mErr)
		}
		fmt.Fprintln(io.Stdout, string(buf))
		return 0
	}
	if len(res.Entries) == 0 {
		fmt.Fprintln(io.Stdout, "no runs configured on this circuit")
		fmt.Fprintln(io.Stdout, "  edit ~/.drift/runs.yaml on the circuit to add entries")
		return 0
	}
	p := style.For(io.Stdout, false)
	rows := make([][]string, 0, len(res.Entries))
	for _, e := range res.Entries {
		rows = append(rows, []string{e.Name, string(e.Mode), e.Description})
	}
	writeTable(io.Stdout, p, []string{"NAME", "MODE", "DESCRIPTION"}, rows, accentCellStyler(0))
	return 0
}

func runRunExec(ctx context.Context, io IO, root *CLI, cmd runCmd, deps deps) int {
	// `drift run` with no name reuses the `drift runs` listing — same RPC,
	// same rendering — so users get a usable error-free path instead of
	// Kong's "expected <name>".
	if cmd.Name == "" {
		return runRunsList(ctx, io, root, runsCmd{}, deps)
	}
	_, circuit, err := resolveCircuit(root, deps)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	var res wire.RunResolveResult
	if err := deps.call(ctx, circuit, wire.MethodRunResolve, wire.RunResolveParams{
		Name: cmd.Name,
		Args: cmd.Args,
	}, &res); err != nil {
		return errfmt.Emit(io.Stderr, err)
	}

	useMosh := res.Mode == wire.RunModeInteractive && !cmd.SSH && moshOnPath()
	bin, argv := buildRunArgv(res.Mode, useMosh, circuit, cmd.ForwardAgent, res.Command)

	p := style.For(io.Stderr, root.Output == "json")
	if p.Enabled {
		transport := "ssh"
		if useMosh {
			transport = "mosh"
		}
		fmt.Fprintln(io.Stderr, p.Dim(fmt.Sprintf("→ %s (%s, via %s)", res.Name, res.Mode, transport)))
	}

	stdio := connect.Stdio{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}
	if execErr := driftexec.Interactive(ctx, bin, argv, stdio.Stdin, stdio.Stdout, stdio.Stderr); execErr != nil {
		// Pass remote exit code through untouched.
		var ee *driftexec.Error
		if errors.As(execErr, &ee) && ee.ExitCode != 0 {
			return ee.ExitCode
		}
		return errfmt.Emit(io.Stderr, execErr)
	}

	// Post-hook runs after a successful remote exit. Failure in the hook
	// is surfaced but doesn't retroactively fail the run itself — the
	// user's remote session already completed.
	if hookErr := runPostHook(ctx, io, root, deps, circuit, res.Post); hookErr != nil {
		return errfmt.Emit(io.Stderr, hookErr)
	}
	return 0
}

// buildRunArgv shapes the ssh/mosh command line for one run invocation.
// Interactive mode asks for a PTY (-t for ssh; mosh always gets one).
// Output mode disables PTY allocation (-T) so the remote command writes
// through uncluttered — important for pipelines like `drift run uptime | grep`.
func buildRunArgv(mode wire.RunMode, useMosh bool, circuit string, forwardAgent bool, remoteCmd string) (string, []string) {
	target := "drift." + circuit
	if useMosh {
		return "mosh", []string{target, "--", "sh", "-c", remoteCmd}
	}
	var args []string
	if mode == wire.RunModeInteractive {
		args = append(args, "-t")
	} else {
		args = append(args, "-T")
	}
	if forwardAgent {
		args = append(args, "-A")
	}
	args = append(args, target, remoteCmd)
	return "ssh", args
}

// runPostHook dispatches the named client-side hook. An unknown hook name
// is treated as a hard error — the server-side registry should never ship
// a hook the client doesn't handle, but if it does we'd rather surface
// the mismatch than silently swallow the handoff.
func runPostHook(ctx context.Context, io IO, root *CLI, deps deps, circuit string, hook wire.RunPostHook) error {
	switch hook {
	case wire.RunPostNone:
		return nil
	case wire.RunPostConnectLastScaffold:
		return connectLastScaffold(ctx, io, root, deps, circuit)
	default:
		return fmt.Errorf("drift run: server returned unknown post hook %q — upgrade drift", hook)
	}
}

// connectLastScaffold reads ~/.drift/last-scaffold over SSH and, if a kart
// name is present, invokes runConnect on it. Missing / empty file is not
// an error — the claude session may have decided not to produce a kart
// (user aborted, etc.) and we exit cleanly.
func connectLastScaffold(ctx context.Context, io IO, root *CLI, deps deps, circuit string) error {
	name, err := readLastScaffold(ctx, circuit)
	if err != nil {
		return fmt.Errorf("drift run scaffolder: read handoff sentinel: %w", err)
	}
	if name == "" {
		p := style.For(io.Stderr, root.Output == "json")
		if p.Enabled {
			fmt.Fprintln(io.Stderr, p.Dim("scaffolder exited without writing ~/.drift/last-scaffold — skipping connect"))
		}
		return nil
	}
	p := style.For(io.Stderr, root.Output == "json")
	if p.Enabled {
		fmt.Fprintln(io.Stderr, p.Dim("→ connecting to scaffolded kart "+name))
	}
	rc := runConnect(ctx, io, root, connectCmd{Name: name}, deps)
	if rc != 0 {
		return fmt.Errorf("drift run scaffolder: auto-connect to %q failed (exit %d)", name, rc)
	}
	return nil
}

// readLastScaffold is a small one-shot ssh that prints the sentinel file
// contents (or "" if missing). Runs as an output-mode child — no PTY, no
// stdin — so its stdout is clean enough to parse.
func readLastScaffold(ctx context.Context, circuit string) (string, error) {
	target := "drift." + circuit
	// test -f … gate avoids ssh-side "cat: ...: No such file" noise when
	// the file is simply absent, which we want to treat as empty.
	remote := `if [ -s "$HOME/.drift/last-scaffold" ]; then cat "$HOME/.drift/last-scaffold"; fi`
	cmd := driftexec.Cmd{
		Name: "ssh",
		Args: []string{"-T", target, remote},
	}
	res, err := driftexec.Run(ctx, cmd)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(res.Stdout)), nil
}

// moshOnPath is shared with what used to be ai.go — a client without mosh
// falls back to ssh regardless of what the circuit supports.
func moshOnPath() bool {
	_, err := osexec.LookPath("mosh")
	return err == nil
}
