package drift

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/charmbracelet/huh"
	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/cli/ui"
	"github.com/kurisu-agent/drift/internal/connect"
	driftexec "github.com/kurisu-agent/drift/internal/exec"
	"github.com/kurisu-agent/drift/internal/wire"
)

// runCmd: `drift run <name> [args…]`. Args are forwarded to the server's
// template expansion. Name is optional so bare `drift run` on a TTY drops
// into the picker; non-TTY callers fall through to the listing the same
// way `drift runs` would. Users who want the print-only surface should use
// `drift runs` directly.
type runCmd struct {
	Name         string   `arg:"" optional:"" help:"Shorthand name (see drift runs); omit to pick interactively."`
	Args         []string `arg:"" optional:"" passthrough:"" help:"Positional args forwarded to the remote command."`
	SSH          bool     `name:"ssh" help:"Force plain SSH (skip mosh) for interactive entries."`
	ForwardAgent bool     `name:"forward-agent" help:"Enable SSH agent forwarding (-A)."`
}

// runRunsList fetches run.list and renders it as a table or JSON. Used
// by `drift runs`, bare `drift run` on a non-TTY, and the zero-entry hint
// inside the interactive picker.
func runRunsList(ctx context.Context, io IO, root *CLI, deps deps) int {
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
	p := ui.NewTheme(io.Stdout, false)
	rows := make([][]string, 0, len(res.Entries))
	for _, e := range res.Entries {
		rows = append(rows, []string{e.Name, string(e.Mode), e.Description})
	}
	writeTable(io.Stdout, p, []string{"NAME", "MODE", "DESCRIPTION"}, rows, accentCellStyler(0))
	return 0
}

func runRunExec(ctx context.Context, io IO, root *CLI, cmd runCmd, deps deps) int {
	// Interactive prompts require a real TTY and a human-readable output
	// channel; pipelines and `--output json` fall through to the scripted
	// listing instead.
	canPrompt := stdinIsTTY(io.Stdin) && root.Output != "json"

	// No name + no TTY: keep the existing "fall through to the listing"
	// affordance so scripted callers get a usable error-free path instead
	// of hanging on a prompt they can't answer.
	if cmd.Name == "" && !canPrompt {
		return runRunsList(ctx, io, root, deps)
	}
	_, circuit, err := resolveCircuit(root, deps)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}

	// Prompt path: no name was typed (pick one from the registry), or a
	// name was typed with no positional args (offer to fill the entry's
	// declared args). CLI positional args always bypass the prompt — a
	// scripted `drift run ping 1.1.1.1` stays a one-shot.
	if canPrompt && len(cmd.Args) == 0 {
		picked, aborted, pErr := pickAndFillRun(ctx, io, circuit, deps, cmd.Name)
		if pErr != nil {
			return errfmt.Emit(io.Stderr, pErr)
		}
		if aborted {
			return 0
		}
		cmd.Name = picked.name
		cmd.Args = picked.args
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

	p := ui.NewTheme(io.Stderr, root.Output == "json")
	if p.Enabled {
		transport := "ssh"
		if useMosh {
			transport = "mosh"
		}
		fmt.Fprintln(io.Stderr, p.Dim(fmt.Sprintf("→ %s (%s, via %s)", res.Name, res.Mode, transport)))
	}

	stdio := connect.Stdio{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}
	if execErr := driftexec.Interactive(ctx, bin, argv, stdio.Stdin, stdio.Stdout, stdio.Stderr); execErr != nil {
		var ee *driftexec.Error
		if errors.As(execErr, &ee) && ee.ExitCode != 0 {
			return ee.ExitCode
		}
		return errfmt.Emit(io.Stderr, execErr)
	}

	if hookErr := runPostHook(ctx, io, root, deps, circuit, res.Post); hookErr != nil {
		return errfmt.Emit(io.Stderr, hookErr)
	}
	return 0
}

// pickedRun carries the outcome of the interactive picker — a chosen entry
// plus whatever args the user supplied through the declared prompts.
type pickedRun struct {
	name string
	args []string
}

// Prompter indirection — tests swap these package-level vars with fakes
// so runRunExec can be exercised without a real TTY.
var (
	pickRunEntryFn = pickRunEntry
	promptOneArgFn = promptOneArg
)

// pickAndFillRun drives the interactive picker.
//
// When nameHint is empty, the user selects an entry from a huh.Select
// sourced from run.list; when nameHint is set, it jumps straight to that
// entry's arg prompts (or falls through with no args if the server does
// not surface it, letting run.resolve produce the canonical not-found
// error). Returns aborted=true when the user cancels — callers should
// treat that as a clean exit.
func pickAndFillRun(ctx context.Context, io IO, circuit string, deps deps, nameHint string) (*pickedRun, bool, error) {
	var list wire.RunListResult
	if err := deps.call(ctx, circuit, wire.MethodRunList, struct{}{}, &list); err != nil {
		return nil, false, err
	}
	if len(list.Entries) == 0 && nameHint == "" {
		fmt.Fprintln(io.Stdout, "no runs configured on this circuit")
		fmt.Fprintln(io.Stdout, "  edit ~/.drift/runs.yaml on the circuit to add entries")
		return nil, true, nil
	}

	var entry *wire.RunEntry
	if nameHint == "" {
		picked, aborted, err := pickRunEntryFn(list.Entries)
		if err != nil || aborted {
			return nil, aborted, err
		}
		entry = picked
	} else {
		for i := range list.Entries {
			if list.Entries[i].Name == nameHint {
				entry = &list.Entries[i]
				break
			}
		}
		if entry == nil {
			// Unknown name: hand back to run.resolve so the server produces
			// the canonical not-found error (shape + type preserved).
			return &pickedRun{name: nameHint}, false, nil
		}
	}

	args, aborted, err := promptEntryArgs(entry)
	if err != nil || aborted {
		return nil, aborted, err
	}
	return &pickedRun{name: entry.Name, args: args}, false, nil
}

func pickRunEntry(entries []wire.RunEntry) (*wire.RunEntry, bool, error) {
	opts := make([]huh.Option[string], 0, len(entries))
	for _, e := range entries {
		label := e.Name
		if e.Description != "" {
			label = fmt.Sprintf("%-14s — %s", e.Name, e.Description)
		}
		opts = append(opts, huh.NewOption(label, e.Name))
	}
	var pick string
	sel := huh.NewSelect[string]().
		Title("drift run").
		Description("Pick a run · type to filter · enter to continue · esc/ctrl+c to quit").
		Options(opts...).
		Filtering(true).
		Height(18).
		Value(&pick)
	if err := huh.NewForm(huh.NewGroup(sel)).Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return nil, true, nil
		}
		return nil, false, err
	}
	for i := range entries {
		if entries[i].Name == pick {
			return &entries[i], false, nil
		}
	}
	return nil, true, nil
}

func promptEntryArgs(entry *wire.RunEntry) ([]string, bool, error) {
	if len(entry.Args) == 0 {
		return nil, false, nil
	}
	args := make([]string, len(entry.Args))
	for i, spec := range entry.Args {
		val, aborted, err := promptOneArgFn(spec)
		if err != nil || aborted {
			return nil, aborted, err
		}
		args[i] = val
	}
	return args, false, nil
}

func promptOneArg(spec wire.RunArgSpec) (string, bool, error) {
	title := spec.Prompt
	if title == "" {
		title = spec.Name
	}
	val := spec.Default
	var field huh.Field
	switch spec.Type {
	case wire.RunArgTypeText:
		field = huh.NewText().Title(title).Value(&val)
	case wire.RunArgTypeSelect:
		opts := make([]huh.Option[string], 0, len(spec.Options))
		for _, opt := range spec.Options {
			opts = append(opts, huh.NewOption(opt, opt))
		}
		field = huh.NewSelect[string]().Title(title).Options(opts...).Value(&val)
	default:
		field = huh.NewInput().Title(title).Value(&val)
	}
	if err := huh.NewForm(huh.NewGroup(field)).Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return "", true, nil
		}
		return "", false, err
	}
	return val, false, nil
}
