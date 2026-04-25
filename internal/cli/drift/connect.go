package drift

import (
	"context"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"sort"
	"time"

	"charm.land/huh/v2"
	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/cli/ui"
	"github.com/kurisu-agent/drift/internal/config"
	"github.com/kurisu-agent/drift/internal/connect"
	driftexec "github.com/kurisu-agent/drift/internal/exec"
	"golang.org/x/sync/errgroup"
)

type connectCmd struct {
	Name         string   `arg:"" optional:"" help:"Kart name; omit on a TTY to pick from a merged circuits + karts list."`
	SSHArgs      []string `arg:"" optional:"" passthrough:"" help:"Extra flags forwarded to ssh (e.g. -- -i ~/.ssh/id_lab). Under mosh, wrapped into --ssh=\"ssh …\" for the bootstrap."`
	SSH          bool     `name:"ssh" help:"Force plain SSH (skip mosh)."`
	ForwardAgent bool     `name:"forward-agent" help:"Enable SSH agent forwarding (-A)."`
	NoForwards   bool     `name:"no-forwards" help:"Skip the workstation-side ports reconcile for this session only."`
}

// circuitKart pairs a listEntry with the circuit it was fetched from, so
// cross-circuit pickers can render and dispatch against both.
type circuitKart struct {
	Circuit string
	Entry   listEntry
}

// connectChoice encodes a row of the merged picker. Kart=="" is a
// circuit-only entry (drop the user into a shell on the host); Kart!="" is
// a regular cross-circuit kart connect.
type connectChoice struct {
	Circuit string
	Kart    string
}

// runConnect is the merged front door. With a positional name, it
// preserves the historical `drift connect <kart>` shape and forwards to
// the kart connect path. With no name on a TTY, it shows the merged
// circuits-plus-karts picker so users can ssh straight into a host or
// pick a kart in one place. Scripts wanting just the listing call
// `drift karts` instead.
func runConnect(ctx context.Context, io IO, root *CLI, cmd connectCmd, deps deps) int {
	// With a name, operate against the target circuit directly (matches
	// the flag-driven `-c <circuit>` override). The name path is identical
	// to `drift kart connect <name>`.
	if cmd.Name != "" {
		_, circuit, err := resolveCircuit(root, deps)
		if err != nil {
			return errfmt.Emit(io.Stderr, err)
		}
		return doConnect(ctx, io, root, deps, circuit, cmd.Name, cmd.SSH, cmd.ForwardAgent, cmd.NoForwards,
			expandCLISSHArgs(cmd.SSHArgs))
	}

	if !stdinIsTTY(io.Stdin) || !stdoutIsTTY(io.Stdout) || root.Output == "json" {
		return errfmt.Emit(io.Stderr,
			errors.New("drift connect requires a kart name (non-interactive)"))
	}
	choice, ok, pErr := pickConnectTarget(ctx, io, deps)
	if pErr != nil {
		return errfmt.Emit(io.Stderr, pErr)
	}
	if !ok {
		return 0
	}
	if choice.Kart == "" {
		return doCircuitConnect(ctx, io, root, choice.Circuit, cmd.SSH, cmd.ForwardAgent,
			expandCLISSHArgs(cmd.SSHArgs))
	}
	return doConnect(ctx, io, root, deps, choice.Circuit, choice.Kart, cmd.SSH, cmd.ForwardAgent, cmd.NoForwards,
		expandCLISSHArgs(cmd.SSHArgs))
}

// runKartConnect is the entry point for `drift kart connect [name]`. It
// is the kart-only variant of the picker — no circuit-only rows — so
// users who want a kart always get a kart.
func runKartConnect(ctx context.Context, io IO, root *CLI, cmd kartConnectCmd, deps deps) int {
	if cmd.Name != "" {
		_, circuit, err := resolveCircuit(root, deps)
		if err != nil {
			return errfmt.Emit(io.Stderr, err)
		}
		return doConnect(ctx, io, root, deps, circuit, cmd.Name, cmd.SSH, cmd.ForwardAgent, cmd.NoForwards,
			expandCLISSHArgs(cmd.SSHArgs))
	}
	if !stdinIsTTY(io.Stdin) || !stdoutIsTTY(io.Stdout) || root.Output == "json" {
		return errfmt.Emit(io.Stderr,
			errors.New("drift kart connect requires a kart name (non-interactive)"))
	}
	pickedCircuit, pickedKart, ok, pErr := pickConnectKart(ctx, io, deps)
	if pErr != nil {
		return errfmt.Emit(io.Stderr, pErr)
	}
	if !ok {
		return 0
	}
	return doConnect(ctx, io, root, deps, pickedCircuit, pickedKart, cmd.SSH, cmd.ForwardAgent, cmd.NoForwards,
		expandCLISSHArgs(cmd.SSHArgs))
}

// runCircuitConnect is the entry point for `drift circuit connect [name]`
// — drops the user into an interactive shell on the circuit's host with
// no kart in between.
func runCircuitConnect(ctx context.Context, io IO, root *CLI, cmd circuitConnectCmd, deps deps) int {
	cfgPath, err := deps.clientConfigPath()
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	cfg, err := config.LoadClient(cfgPath)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	if cmd.Name != "" {
		if _, ok := cfg.Circuits[cmd.Name]; !ok {
			return errfmt.Emit(io.Stderr, fmt.Errorf("circuit %q not configured", cmd.Name))
		}
		return doCircuitConnect(ctx, io, root, cmd.Name, cmd.SSH, cmd.ForwardAgent,
			expandCLISSHArgs(cmd.SSHArgs))
	}
	if !stdinIsTTY(io.Stdin) || !stdoutIsTTY(io.Stdout) || root.Output == "json" {
		return errfmt.Emit(io.Stderr,
			errors.New("drift circuit connect requires a circuit name (non-interactive)"))
	}
	picked, ok, pErr := pickConnectCircuit(io, deps)
	if pErr != nil {
		return errfmt.Emit(io.Stderr, pErr)
	}
	if !ok {
		return 0
	}
	return doCircuitConnect(ctx, io, root, picked, cmd.SSH, cmd.ForwardAgent,
		expandCLISSHArgs(cmd.SSHArgs))
}

// pickConnectKart is the connect-flavored cross-circuit kart picker.
// Thin wrapper around pickKartAcrossCircuits that supplies the title /
// description tuned for `drift kart connect`.
func pickConnectKart(ctx context.Context, io IO, deps deps) (string, string, bool, error) {
	return pickKartAcrossCircuits(ctx, io, deps,
		"drift connect — pick a kart",
		"type to filter · enter to connect · esc to cancel")
}

// pickKartAcrossCircuits fetches kart.list from every configured circuit
// in parallel and renders a filterable huh.Select ordered by last-used
// descending. Caller supplies the prompt strings so the picker fits the
// invoking verb (`drift connect`, `drift delete`, `drift start`, …).
// Returns (circuit, kart, true, nil) on selection, (_, _, false, nil) on
// abort / empty lists (the latter prints its own notice), err on fatal
// failure.
func pickKartAcrossCircuits(ctx context.Context, io IO, deps deps, title, description string) (string, string, bool, error) {
	cfgPath, err := deps.clientConfigPath()
	if err != nil {
		return "", "", false, err
	}
	cfg, err := config.LoadClient(cfgPath)
	if err != nil {
		return "", "", false, err
	}
	if len(cfg.Circuits) == 0 {
		fmt.Fprintln(io.Stderr, "no circuits configured (try `drift circuit add user@host`)")
		return "", "", false, nil
	}

	karts, probeErrs := collectCircuitKarts(ctx, cfg, deps)
	// Surface per-circuit probe failures on stderr so the picker can
	// still launch on a partial set, but users see what's missing.
	for circuit, perr := range probeErrs {
		fmt.Fprintf(io.Stderr, "warning: %s: %v\n", circuit, perr)
	}
	if len(karts) == 0 {
		fmt.Fprintln(io.Stderr, "no karts found on any configured circuit")
		return "", "", false, nil
	}

	sortByLastUsedDesc(karts)
	opts := make([]huh.Option[string], 0, len(karts))
	for i, k := range karts {
		opts = append(opts, huh.NewOption(formatCircuitKartOption(k, cfg.DefaultCircuit), fmt.Sprintf("%d", i)))
	}
	var pick string
	sel := huh.NewSelect[string]().
		Title(title).
		Description(description).
		Options(opts...).
		Filtering(true).
		Height(18).
		Value(&pick)
	if err := huh.NewForm(huh.NewGroup(sel)).Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return "", "", false, nil
		}
		return "", "", false, err
	}
	var idx int
	if _, err := fmt.Sscanf(pick, "%d", &idx); err != nil || idx < 0 || idx >= len(karts) {
		return "", "", false, nil
	}
	return karts[idx].Circuit, karts[idx].Entry.Name, true, nil
}

// resolveKartTarget produces the (circuit, kart) pair for a lifecycle
// verb that supports the picker fallback (`drift start/stop/delete`).
// With an explicit name, returns the configured default circuit (or -c
// override) paired with that name. With an empty name on a TTY, drops
// into the cross-circuit picker. Non-interactive callers without a name
// get a hard error so scripts fail fast.
//
// The bool reports whether a target was resolved; on false the int is
// the exit code the caller should return (0 for clean abort, non-zero
// for an error already printed to stderr).
func resolveKartTarget(ctx context.Context, io IO, root *CLI, deps deps, name, verb string) (string, string, bool, int) {
	if name != "" {
		_, circuit, err := resolveCircuit(root, deps)
		if err != nil {
			return "", "", false, errfmt.Emit(io.Stderr, err)
		}
		return circuit, name, true, 0
	}
	if !stdinIsTTY(io.Stdin) || !stdoutIsTTY(io.Stdout) || root.Output == "json" {
		return "", "", false, errfmt.Emit(io.Stderr,
			fmt.Errorf("%s requires a kart name (non-interactive)", verb))
	}
	circuit, kart, picked, err := pickKartAcrossCircuits(ctx, io, deps,
		verb+" — pick a kart",
		"type to filter · enter to confirm · esc to cancel")
	if err != nil {
		return "", "", false, errfmt.Emit(io.Stderr, err)
	}
	if !picked {
		return "", "", false, 0
	}
	return circuit, kart, true, 0
}

// collectCircuitKarts fans out kart.list across every configured circuit
// in parallel (same shape as status's probe fanout). Per-circuit errors
// are returned in the second map rather than aborting — a single
// unreachable circuit shouldn't break the picker.
func collectCircuitKarts(ctx context.Context, cfg *config.Client, deps deps) ([]circuitKart, map[string]error) {
	names := make([]string, 0, len(cfg.Circuits))
	for n := range cfg.Circuits {
		names = append(names, n)
	}
	sort.Strings(names)

	type result struct {
		entries []listEntry
		err     error
	}
	results := make([]result, len(names))
	var g errgroup.Group
	g.SetLimit(4)
	for i, n := range names {
		g.Go(func() error {
			entries, _, err := fetchKartList(ctx, deps, n)
			results[i] = result{entries: entries, err: err}
			return nil
		})
	}
	_ = g.Wait()

	var all []circuitKart
	errs := make(map[string]error)
	for i, n := range names {
		if results[i].err != nil {
			errs[n] = results[i].err
			continue
		}
		for _, e := range results[i].entries {
			all = append(all, circuitKart{Circuit: n, Entry: e})
		}
	}
	return all, errs
}

// sortByLastUsedDesc orders karts so the most recently-used one floats to
// the top. Missing/unparseable timestamps sink to the bottom; ties break
// alphabetically by circuit then kart name so the display is stable.
func sortByLastUsedDesc(karts []circuitKart) {
	parse := func(s string) (time.Time, bool) {
		if s == "" {
			return time.Time{}, false
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
			if t, err := time.Parse(layout, s); err == nil {
				return t, true
			}
		}
		return time.Time{}, false
	}
	sort.SliceStable(karts, func(i, j int) bool {
		ti, iOK := parse(karts[i].Entry.LastUsed)
		tj, jOK := parse(karts[j].Entry.LastUsed)
		if iOK && jOK {
			if !ti.Equal(tj) {
				return ti.After(tj)
			}
		} else if iOK != jOK {
			return iOK
		}
		if karts[i].Circuit != karts[j].Circuit {
			return karts[i].Circuit < karts[j].Circuit
		}
		return karts[i].Entry.Name < karts[j].Entry.Name
	})
}

// formatCircuitKartOption builds one row of the cross-circuit picker.
// Columns: circuit, kart name, status, last-used (humanized), source.
// The default circuit gets a chevron prefix so the eye picks it out.
func formatCircuitKartOption(k circuitKart, defaultCircuit string) string {
	marker := "  "
	if k.Circuit == defaultCircuit {
		marker = "→ "
	}
	status := k.Entry.Status
	if k.Entry.Stale {
		status += " (stale)"
	}
	src := k.Entry.Source.Mode
	if k.Entry.Source.URL != "" {
		src = k.Entry.Source.Mode + " " + driftexec.RedactSecrets(k.Entry.Source.URL)
	}
	return fmt.Sprintf("%s%-14s  %-20s  %-18s  %-12s  %s",
		marker,
		k.Circuit,
		k.Entry.Name,
		"("+status+")",
		humanizeLastUsed(k.Entry.LastUsed),
		src,
	)
}

// humanizeLastUsed renders a relative duration string ("2h ago", "3d ago",
// "2026-01-05" for >30 days) from an RFC3339 timestamp. Empty input
// becomes "never used" so picker rows line up visually.
func humanizeLastUsed(ts string) string {
	if ts == "" {
		return "never used"
	}
	var t time.Time
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, ts); err == nil {
			t = parsed
			break
		}
	}
	if t.IsZero() {
		return ts
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("2006-01-02")
	}
}

// pickConnectTarget renders the merged picker used by bare `drift
// connect`: configured circuits at the top (each row is "ssh straight in
// to the host") followed by the cross-circuit kart roster sorted by
// last-used. Returns (choice, true, nil) on selection, (_, false, nil) on
// abort or empty config (the empty path prints its own notice).
func pickConnectTarget(ctx context.Context, io IO, deps deps) (connectChoice, bool, error) {
	cfgPath, err := deps.clientConfigPath()
	if err != nil {
		return connectChoice{}, false, err
	}
	cfg, err := config.LoadClient(cfgPath)
	if err != nil {
		return connectChoice{}, false, err
	}
	if len(cfg.Circuits) == 0 {
		fmt.Fprintln(io.Stderr, "no circuits configured (try `drift circuit add user@host`)")
		return connectChoice{}, false, nil
	}

	karts, probeErrs := collectCircuitKarts(ctx, cfg, deps)
	for circuit, perr := range probeErrs {
		fmt.Fprintf(io.Stderr, "warning: %s: %v\n", circuit, perr)
	}
	sortByLastUsedDesc(karts)

	circuits := sortedCircuitNames(cfg)

	// Flat choice slice + parallel options slice — huh.NewSelect works
	// on string values, so we use the slice index as the value and look
	// the choice back up after the form returns.
	choices := make([]connectChoice, 0, len(circuits)+len(karts))
	opts := make([]huh.Option[string], 0, len(circuits)+len(karts))
	for _, n := range circuits {
		choices = append(choices, connectChoice{Circuit: n})
		opts = append(opts, huh.NewOption(formatCircuitOption(n, cfg.Circuits[n].Host, cfg.DefaultCircuit), fmt.Sprintf("%d", len(choices)-1)))
	}
	for _, k := range karts {
		choices = append(choices, connectChoice{Circuit: k.Circuit, Kart: k.Entry.Name})
		opts = append(opts, huh.NewOption(formatCircuitKartOption(k, cfg.DefaultCircuit), fmt.Sprintf("%d", len(choices)-1)))
	}

	var pick string
	sel := huh.NewSelect[string]().
		Title("drift connect — pick a circuit or kart").
		Description("type to filter · enter to connect · esc to cancel").
		Options(opts...).
		Filtering(true).
		Height(18).
		Value(&pick)
	if err := huh.NewForm(huh.NewGroup(sel)).Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return connectChoice{}, false, nil
		}
		return connectChoice{}, false, err
	}
	var idx int
	if _, err := fmt.Sscanf(pick, "%d", &idx); err != nil || idx < 0 || idx >= len(choices) {
		return connectChoice{}, false, nil
	}
	return choices[idx], true, nil
}

// pickConnectCircuit renders the circuit-only picker for `drift circuit
// connect` — no kart probes, so it returns instantly even when one
// circuit is offline.
func pickConnectCircuit(io IO, deps deps) (string, bool, error) {
	cfgPath, err := deps.clientConfigPath()
	if err != nil {
		return "", false, err
	}
	cfg, err := config.LoadClient(cfgPath)
	if err != nil {
		return "", false, err
	}
	if len(cfg.Circuits) == 0 {
		fmt.Fprintln(io.Stderr, "no circuits configured (try `drift circuit add user@host`)")
		return "", false, nil
	}
	circuits := sortedCircuitNames(cfg)
	opts := make([]huh.Option[string], 0, len(circuits))
	for _, n := range circuits {
		opts = append(opts, huh.NewOption(formatCircuitOption(n, cfg.Circuits[n].Host, cfg.DefaultCircuit), n))
	}
	pick := cfg.DefaultCircuit
	sel := huh.NewSelect[string]().
		Title("drift circuit connect — pick a circuit").
		Description("type to filter · enter to connect · esc to cancel").
		Options(opts...).
		Filtering(true).
		Height(12).
		Value(&pick)
	if err := huh.NewForm(huh.NewGroup(sel)).Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return "", false, nil
		}
		return "", false, err
	}
	return pick, true, nil
}

// sortedCircuitNames returns the configured circuit names with the
// default circuit first (so it lines up with the `→` marker the picker
// prints), then the rest in alphabetical order.
func sortedCircuitNames(cfg *config.Client) []string {
	names := make([]string, 0, len(cfg.Circuits))
	for n := range cfg.Circuits {
		names = append(names, n)
	}
	sort.Strings(names)
	if cfg.DefaultCircuit == "" {
		return names
	}
	out := make([]string, 0, len(names))
	out = append(out, cfg.DefaultCircuit)
	for _, n := range names {
		if n != cfg.DefaultCircuit {
			out = append(out, n)
		}
	}
	return out
}

// formatCircuitOption renders one circuit-only row of the merged /
// circuit-only picker. Mirrors formatCircuitKartOption's column widths
// where possible so the two row types align in the same select.
func formatCircuitOption(circuit, host, defaultCircuit string) string {
	marker := "  "
	if circuit == defaultCircuit {
		marker = "→ "
	}
	return fmt.Sprintf("%s%-14s  %-20s  %-18s  %-12s  %s",
		marker,
		circuit,
		"(circuit shell)",
		"",
		"",
		host,
	)
}

// doCircuitConnect spawns an interactive shell on the circuit's host via
// the SSH alias `drift.<circuit>` (mosh-preferred). No remote command is
// passed, so the user lands in their login shell on the circuit. Mirrors
// doConnect's transport logic, including ssh_args forwarding — on mosh
// the args get wrapped into --ssh="ssh …" so they apply to the ssh
// bootstrap rather than being dropped.
func doCircuitConnect(ctx context.Context, io IO, root *CLI, circuit string, forceSSH, forwardAgent bool, sshArgs []string) int {
	transport := connect.Transport(osexec.LookPath, forceSSH)
	target := "drift." + circuit

	bin := "ssh"
	var argv []string
	if transport == "mosh" {
		moshArgv := []string{"mosh"}
		if len(sshArgs) > 0 {
			moshArgv = append(moshArgv, "--ssh="+connect.BuildMoshSSHOverride(sshArgs))
		}
		moshArgv = append(moshArgv, target)
		// Locale-strip the mosh invocation so the perl wrapper doesn't
		// forward LANG/LC_* to the circuit (typically absent locales,
		// glibc noise; the circuit's own defaults are what we want).
		bin, argv = connect.WrapMoshForLocaleStrip(moshArgv)
	} else {
		argv = []string{"-t"}
		if forwardAgent {
			argv = append(argv, "-A")
		}
		argv = append(argv, sshArgs...)
		argv = append(argv, target)
	}

	p := ui.NewTheme(io.Stderr, root.Output == "json")
	if p.Enabled {
		fmt.Fprintln(io.Stderr, p.Dim(fmt.Sprintf("→ circuit %s (shell, via %s)", circuit, transport)))
	}

	err := driftexec.Interactive(ctx, bin, argv, os.Stdin, os.Stdout, os.Stderr)
	if err == nil {
		return 0
	}
	var ee *driftexec.Error
	if errors.As(err, &ee) && ee.ExitCode != 0 {
		return ee.ExitCode
	}
	return errfmt.Emit(io.Stderr, err)
}

// doConnect is the shared body behind `drift connect` and the post-create
// auto-connect path of `drift new`. Both paths have already resolved the
// circuit, so the helper takes it as a parameter instead of re-resolving.
// sshArgs holds the already-merged (config + CLI passthrough) flag list.
func doConnect(ctx context.Context, io IO, root *CLI, deps deps, circuit, name string, forceSSH, forwardAgent, noForwards bool, sshArgs []string) int {
	// Drift-check preamble: if the kart's tune has changed since the
	// container was built, give the user a chance to rebuild before
	// connecting. Non-TTY or json paths print a warning and proceed.
	if err := maybePromptRebuild(ctx, io.Stderr,
		stdinIsTTY(io.Stdin), stdoutIsTTY(io.Stdout), root.Output == "json",
		deps, circuit, name); err != nil {
		return errfmt.Emit(io.Stderr, err)
	}

	transport := connect.Transport(osexec.LookPath, forceSSH)
	tt := ui.NewTheme(io.Stderr, root.Output == "json")
	sp := tt.NewSpinner(io.Stderr, ui.SpinnerOptions{
		Message:   "connecting to kart \"" + name + "\"",
		Transport: transport,
	})
	d := connect.Deps{
		Call: deps.call,
		// Stop the spinner right before Exec takes the TTY so it doesn't
		// race the interactive child for cursor control.
		OnReady:    sp.Stop,
		BeforeExec: makeBeforeExecPortsHook(io, root, circuit, name, noForwards),
	}
	opts := connect.Options{
		Circuit:      circuit,
		Kart:         name,
		ForceSSH:     forceSSH,
		ForwardAgent: forwardAgent,
		SSHArgs:      sshArgs,
	}
	stdio := connect.Stdio{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}

	// Transport hint to stderr so stdout stays clean for the remote
	// session. Silenced in JSON mode / non-TTY via palette gating.
	if tt.Enabled {
		fmt.Fprintln(io.Stderr, tt.Dim("via "+transport))
	}

	err := connect.Run(ctx, d, opts, stdio)
	// If Run returned before reaching Exec (RPC error), the spinner is
	// still running — make sure it cleans up before errfmt writes.
	sp.Stop()
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
