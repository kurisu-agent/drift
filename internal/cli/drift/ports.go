package drift

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/cli/ui"
	driftexec "github.com/kurisu-agent/drift/internal/exec"
	"github.com/kurisu-agent/drift/internal/ports"
	"github.com/kurisu-agent/drift/internal/wire"
)

// portsCmd is the `drift ports …` namespace. Bare `drift ports` falls
// through to List so the scriptable surface and the human-friendly one
// are the same; a bubbletea TUI front-end is planned but lives in a
// follow-up PR (see plan 13's TUI section for the intended layout).
type portsCmd struct {
	List   portsListCmd   `cmd:"" default:"withargs" help:"Show forwards (table or JSON; bare \"drift ports\" runs this)."`
	Add    portsAddCmd    `cmd:"" help:"Add a forward (remaps on local-port conflict)."`
	Rm     portsRmCmd     `cmd:"" name:"rm" aliases:"remove,delete" help:"Remove a forward."`
	Remap  portsRemapCmd  `cmd:"" help:"Change the local port for an existing forward (REMOTE:LOCAL)."`
	Probe  portsProbeCmd  `cmd:"" help:"Probe in-kart listeners and pick which to forward."`
	Up     portsUpCmd     `cmd:"" help:"Reconcile state to live forwards (no-op when already in sync)."`
	Down   portsDownCmd   `cmd:"" help:"Cancel forwards and stop the master (per-kart, or all when --all)."`
	Status portsStatusCmd `cmd:"" help:"Print state + per-kart master liveness as JSON."`
}

type portsListCmd struct {
	Kart string `name:"kart" help:"Restrict to one kart (\"<circuit>/<kart>\" or just \"<kart>\" with -c)."`
}

type portsAddCmd struct {
	Port  int    `arg:"" help:"In-kart port to forward."`
	Local int    `name:"local" help:"Workstation port (defaults to PORT; auto-remaps on conflict)."`
	Kart  string `name:"kart" help:"\"<circuit>/<kart>\" or just \"<kart>\" with -c."`
}

type portsRmCmd struct {
	Port int    `arg:"" help:"In-kart port (or local port if not found as remote)."`
	Kart string `name:"kart" help:"\"<circuit>/<kart>\" or just \"<kart>\" with -c."`
}

type portsRemapCmd struct {
	Spec string `arg:"" help:"REMOTE:LOCAL — change the workstation port for an existing forward."`
	Kart string `name:"kart" help:"\"<circuit>/<kart>\" or just \"<kart>\" with -c."`
}

type portsUpCmd struct {
	Kart string `name:"kart" help:"Limit reconcile to one kart."`
}

type portsDownCmd struct {
	Kart string `name:"kart" help:"\"<circuit>/<kart>\" — required unless --all."`
	All  bool   `name:"all" help:"Tear down every forward in the state file."`
}

type portsStatusCmd struct{}

// resolvePortsKart parses the --kart flag (or root -c) into a (circuit,
// kart) pair. Accepts "<circuit>/<kart>" as a complete spec, or a bare
// kart name combined with the resolved circuit. Empty string with a TTY
// drops into the cross-circuit picker; off-TTY it errors so scripts fail
// fast.
func resolvePortsKart(ctx context.Context, io IO, root *CLI, deps deps, kartFlag, verb string) (string, string, bool, int) {
	if kartFlag != "" {
		if c, k, ok := ports.SplitKartKey(kartFlag); ok {
			return c, k, true, 0
		}
		// Bare kart name — pair with the resolved circuit.
		_, circuit, err := resolveCircuit(root, deps)
		if err != nil {
			return "", "", false, errfmt.Emit(io.Stderr, err)
		}
		return circuit, kartFlag, true, 0
	}
	return resolveKartTarget(ctx, io, root, deps, "", verb)
}

func loadPortsState(io IO) (string, *ports.State, int) {
	path, err := ports.DefaultPath()
	if err != nil {
		return "", nil, errfmt.Emit(io.Stderr, err)
	}
	state, err := ports.Load(path)
	if err != nil {
		return "", nil, errfmt.Emit(io.Stderr, err)
	}
	return path, state, 0
}

func runPortsList(ctx context.Context, io IO, root *CLI, cmd portsListCmd, deps deps) int {
	_, state, code := loadPortsState(io)
	if state == nil {
		return code
	}

	// Filter to one kart when --kart is set. We allow this without
	// going through the picker because List is read-only and harmless.
	filter := cmd.Kart
	if filter != "" {
		if _, _, ok := ports.SplitKartKey(filter); !ok {
			_, circuit, err := resolveCircuit(root, deps)
			if err != nil {
				return errfmt.Emit(io.Stderr, err)
			}
			filter = ports.KartKey(circuit, filter)
		}
	}

	driver := ports.NewSSHDriver(driftexec.DefaultRunner)
	type row struct {
		Kart   string          `json:"kart"`
		Master masterLiveness  `json:"master"`
		Fwds   []ports.Forward `json:"forwards"`
	}
	keys := make([]string, 0, len(state.Forwards))
	for k := range state.Forwards {
		if filter != "" && k != filter {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	rows := make([]row, 0, len(keys))
	for _, k := range keys {
		c, kt, _ := ports.SplitKartKey(k)
		alive, _ := driver.Check(ctx, ports.SSHHost(c, kt))
		rows = append(rows, row{
			Kart:   k,
			Master: masterLiveness{Alive: alive},
			Fwds:   state.Forwards[k],
		})
	}

	if root.Output == "json" {
		buf, err := json.MarshalIndent(rows, "", "  ")
		if err != nil {
			return errfmt.Emit(io.Stderr, err)
		}
		fmt.Fprintln(io.Stdout, string(buf))
		return 0
	}

	if len(rows) == 0 {
		fmt.Fprintln(io.Stdout, "no forwards configured")
		return 0
	}
	p := ui.NewTheme(io.Stdout, false)
	for i, r := range rows {
		if i > 0 {
			fmt.Fprintln(io.Stdout)
		}
		liveness := "down"
		if r.Master.Alive {
			liveness = "live"
		}
		fmt.Fprintf(io.Stdout, "%s  master: %s\n", p.Bold(r.Kart), liveness)
		for _, f := range r.Fwds {
			line := fmt.Sprintf("  %d → :%d", f.Local, f.Remote)
			if f.RemappedFrom != 0 {
				line += fmt.Sprintf(" (remapped from %d)", f.RemappedFrom)
			}
			if f.Source != "" {
				line += "  " + p.Dim("source: "+string(f.Source))
			}
			fmt.Fprintln(io.Stdout, line)
		}
	}
	return 0
}

type masterLiveness struct {
	Alive bool `json:"alive"`
}

// mutatePortsState is the shared skeleton behind add / rm / remap:
// resolve the target kart, load ports.yaml, run the per-verb mutation,
// save, and reconcile. Each caller supplies the mutation + the summary
// line printed on success. When mutate returns `done=true` the helper
// skips both Save and reconcile (used by the add-is-a-no-op path).
func mutatePortsState(
	ctx context.Context,
	io IO,
	root *CLI,
	cmd portsKartFlag,
	deps deps,
	verb string,
	mutate func(state *ports.State, circuit, kart string) (summary string, done bool, err error),
) int {
	circuit, kart, ok, code := resolvePortsKart(ctx, io, root, deps, cmd.kartFlag(), verb)
	if !ok {
		return code
	}
	path, state, code := loadPortsState(io)
	if state == nil {
		return code
	}
	summary, done, err := mutate(state, circuit, kart)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	if summary != "" {
		fmt.Fprintln(io.Stdout, summary)
	}
	if done {
		return 0
	}
	if err := ports.Save(path, state); err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	return reconcileForKart(ctx, io, root, deps, circuit, kart)
}

// portsKartFlag is the shared surface of the per-verb command structs —
// all of them carry a --kart string. Keeps mutatePortsState from having
// to inspect the command type.
type portsKartFlag interface{ kartFlag() string }

func (c portsAddCmd) kartFlag() string   { return c.Kart }
func (c portsRmCmd) kartFlag() string    { return c.Kart }
func (c portsRemapCmd) kartFlag() string { return c.Kart }

func runPortsAdd(ctx context.Context, io IO, root *CLI, cmd portsAddCmd, deps deps) int {
	return mutatePortsState(ctx, io, root, cmd, deps, "drift ports add",
		func(state *ports.State, circuit, kart string) (string, bool, error) {
			res, err := ports.AddForward(state, ports.DefaultLocalProber, circuit, kart, cmd.Port, cmd.Local, ports.SourceExplicit)
			if err != nil {
				return "", false, err
			}
			if res.NoOp {
				return fmt.Sprintf("%s/%s: %d → :%d (already configured)",
					circuit, kart, res.Forward.Local, res.Forward.Remote), true, nil
			}
			if res.Remapped {
				return fmt.Sprintf("remapped %d → :%d (workstation port %d in use)",
					res.Forward.Local, res.Forward.Remote, res.Forward.RemappedFrom), false, nil
			}
			return fmt.Sprintf("added %s/%s: %d → :%d", circuit, kart, res.Forward.Local, res.Forward.Remote), false, nil
		})
}

func runPortsRm(ctx context.Context, io IO, root *CLI, cmd portsRmCmd, deps deps) int {
	return mutatePortsState(ctx, io, root, cmd, deps, "drift ports rm",
		func(state *ports.State, circuit, kart string) (string, bool, error) {
			removed, err := ports.RemoveForward(state, circuit, kart, cmd.Port)
			if err != nil {
				return "", false, err
			}
			return fmt.Sprintf("removed %s/%s: %d → :%d", circuit, kart, removed.Local, removed.Remote), false, nil
		})
}

func runPortsRemap(ctx context.Context, io IO, root *CLI, cmd portsRemapCmd, deps deps) int {
	parts := strings.SplitN(cmd.Spec, ":", 2)
	if len(parts) != 2 {
		return errfmt.Emit(io.Stderr, errors.New("drift ports remap: spec must be REMOTE:LOCAL"))
	}
	remote, err := strconv.Atoi(parts[0])
	if err != nil {
		return errfmt.Emit(io.Stderr, fmt.Errorf("drift ports remap: bad remote port: %w", err))
	}
	local, err := strconv.Atoi(parts[1])
	if err != nil {
		return errfmt.Emit(io.Stderr, fmt.Errorf("drift ports remap: bad local port: %w", err))
	}
	return mutatePortsState(ctx, io, root, cmd, deps, "drift ports remap",
		func(state *ports.State, circuit, kart string) (string, bool, error) {
			got, err := ports.RemapForward(state, ports.DefaultLocalProber, circuit, kart, remote, local)
			if err != nil {
				return "", false, err
			}
			return fmt.Sprintf("remapped %s/%s: %d → :%d", circuit, kart, got.Local, got.Remote), false, nil
		})
}

func runPortsUp(ctx context.Context, io IO, root *CLI, cmd portsUpCmd, deps deps) int {
	opts := ports.ReconcileOptions{}
	if cmd.Kart != "" {
		c, k, ok := ports.SplitKartKey(cmd.Kart)
		if !ok {
			_, circuit, err := resolveCircuit(root, deps)
			if err != nil {
				return errfmt.Emit(io.Stderr, err)
			}
			c, k = circuit, cmd.Kart
		}
		opts.OnlyKart = ports.KartKey(c, k)
	}
	return runReconcile(ctx, io, root, opts)
}

func runPortsDown(ctx context.Context, io IO, root *CLI, cmd portsDownCmd, deps deps) int {
	if !cmd.All && cmd.Kart == "" {
		return errfmt.Emit(io.Stderr, errors.New("drift ports down: --kart or --all is required"))
	}
	path, state, code := loadPortsState(io)
	if state == nil {
		return code
	}

	// Down works by emptying state for the targeted kart(s) before
	// reconcile — that's what makes it persist across the next `drift
	// connect` (which just re-imports devcontainer entries; an explicit
	// `down` doesn't survive devcontainer passthrough). Use `rm` if you
	// want to remove a single forward; `down` is the bigger hammer.
	if cmd.All {
		state.Forwards = nil
	} else {
		c, k, ok := ports.SplitKartKey(cmd.Kart)
		if !ok {
			_, circuit, err := resolveCircuit(root, deps)
			if err != nil {
				return errfmt.Emit(io.Stderr, err)
			}
			c, k = circuit, cmd.Kart
		}
		state.Delete(c, k)
	}
	if err := ports.Save(path, state); err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	opts := ports.ReconcileOptions{}
	if !cmd.All {
		c, k, _ := splitOrPair(cmd.Kart, root, deps)
		opts.OnlyKart = ports.KartKey(c, k)
	}
	return runReconcile(ctx, io, root, opts)
}

func runPortsStatus(ctx context.Context, io IO, root *CLI, _ portsStatusCmd, _ deps) int {
	_, state, code := loadPortsState(io)
	if state == nil {
		return code
	}
	driver := ports.NewSSHDriver(driftexec.DefaultRunner)
	type kartStatus struct {
		Kart     string          `json:"kart"`
		Alive    bool            `json:"master_alive"`
		Forwards []ports.Forward `json:"forwards"`
	}
	keys := make([]string, 0, len(state.Forwards))
	for k := range state.Forwards {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]kartStatus, 0, len(keys))
	for _, k := range keys {
		c, kt, _ := ports.SplitKartKey(k)
		alive, _ := driver.Check(ctx, ports.SSHHost(c, kt))
		out = append(out, kartStatus{Kart: k, Alive: alive, Forwards: state.Forwards[k]})
	}
	buf, err := json.MarshalIndent(map[string]any{"version": state.Version, "karts": out}, "", "  ")
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	fmt.Fprintln(io.Stdout, string(buf))
	return 0
}

// reconcileForKart is the post-mutation kicker used by add/rm/remap.
// Pre-checks kart status via kart.info: a stopped kart can't accept an
// `ssh -M` master (the proxy command's `devpod ssh --stdio` exits
// immediately, surfacing as ssh's cryptic "Connection closed by UNKNOWN
// port 65535"), so we'd rather print one friendly line than that wall
// of error. The state-file mutation already landed; the forward will
// activate the next time the kart starts (via `drift start` /
// `drift connect`'s own pre-exec reconcile).
func reconcileForKart(ctx context.Context, io IO, root *CLI, deps deps, circuit, kart string) int {
	if !kartRunningOrWarn(ctx, io, deps, circuit, kart) {
		return 0
	}
	return runReconcile(ctx, io, root, ports.ReconcileOptions{OnlyKart: ports.KartKey(circuit, kart)})
}

// kartRunningOrWarn calls kart.info and prints a friendly note when the
// kart is anything but "running". Returns true iff reconcile should
// proceed. RPC failures fall through to "true" so a transient circuit
// outage doesn't block the local mutation entirely — reconcile will
// then either succeed or surface its own (slightly less friendly) error.
func kartRunningOrWarn(ctx context.Context, io IO, deps deps, circuit, kart string) bool {
	var info struct {
		Status string `json:"status"`
	}
	if err := deps.call(ctx, circuit, wire.MethodKartInfo,
		map[string]string{"name": kart}, &info); err != nil {
		return true
	}
	if info.Status == "running" {
		return true
	}
	fmt.Fprintf(io.Stderr,
		"%s/%s is %s; forward will activate on next `drift start` or `drift connect`\n",
		circuit, kart, info.Status)
	return false
}

func runReconcile(ctx context.Context, io IO, root *CLI, opts ports.ReconcileOptions) int {
	statePath, err := ports.DefaultPath()
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	driver := ports.NewSSHDriver(driftexec.DefaultRunner)
	report, err := ports.LoadAndReconcile(ctx, statePath, driver, opts)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	p := ui.NewTheme(io.Stderr, root.Output == "json")
	for _, change := range report.Changes {
		if p.Enabled {
			fmt.Fprintln(io.Stderr, p.Dim(change))
		}
	}
	for _, e := range report.Errors {
		fmt.Fprintf(io.Stderr, "warning: %v\n", e)
	}
	return 0
}

// splitOrPair: helper for --kart parsing in down. Returns (circuit, kart, ok).
// On format error falls back to pairing with the resolved circuit.
func splitOrPair(kartFlag string, root *CLI, deps deps) (string, string, bool) {
	if c, k, ok := ports.SplitKartKey(kartFlag); ok {
		return c, k, true
	}
	_, circuit, err := resolveCircuit(root, deps)
	if err != nil {
		return "", kartFlag, false
	}
	return circuit, kartFlag, true
}
