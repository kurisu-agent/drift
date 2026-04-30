package drift

import (
	"context"
	"errors"
	"fmt"

	"github.com/charmbracelet/huh"

	"github.com/kurisu-agent/drift/internal/cli/style"
	driftexec "github.com/kurisu-agent/drift/internal/exec"
	"github.com/kurisu-agent/drift/internal/ports"
)

// makeBeforeExecPortsHook returns the BeforeExec callback `drift connect`
// hands to internal/connect. The hook unions the kart's resolved
// devcontainer forwardPorts into ports.yaml, prompts on conflicts when
// the session is interactive, and reconciles for that kart only.
//
// Best-effort by design: a failed reconcile warns on stderr but does not
// abort the connect — the user's shell is the primary goal, the
// forwards are a pleasant secondary. `disabled=true` (from --no-forwards
// or `auto_forward_ports: false`) returns a no-op hook so the session
// stays out of ports.yaml entirely.
func makeBeforeExecPortsHook(io IO, root *CLI, circuit, kart string, disabled bool) func(context.Context, []int) error {
	if disabled {
		return func(context.Context, []int) error { return nil }
	}
	return func(ctx context.Context, forwardPorts []int) error {
		statePath, err := ports.DefaultPath()
		if err != nil {
			fmt.Fprintf(io.Stderr, "warning: ports: %v\n", err)
			return nil
		}
		state, err := ports.Load(statePath)
		if err != nil {
			fmt.Fprintf(io.Stderr, "warning: ports: %v\n", err)
			return nil
		}
		// Fast path: nothing to do. Avoids Save + reconcile + ssh check
		// for users who never forward anything on this kart.
		if len(forwardPorts) == 0 && len(state.Get(circuit, kart)) == 0 {
			return nil
		}
		if len(forwardPorts) > 0 {
			interactive := stdinIsTTY(io.Stdin) && stdoutIsTTY(io.Stdout) && root.Output != "json"
			var prompt remapPromptFunc
			if interactive {
				prompt = promptRemapConflict
			}
			if err := unionDevcontainerWithPrompts(io, state, ports.DefaultLocalProber, circuit, kart, forwardPorts, prompt); err != nil {
				fmt.Fprintf(io.Stderr, "warning: ports: %v\n", err)
				// Fall through — reconcile what we have.
			}
			if err := ports.Save(statePath, state); err != nil {
				fmt.Fprintf(io.Stderr, "warning: ports: save: %v\n", err)
				return nil
			}
		}
		if len(state.Get(circuit, kart)) == 0 {
			return nil
		}
		driver := ports.NewSSHDriver(driftexec.DefaultRunner)
		report, err := ports.LoadAndReconcile(ctx, statePath, driver,
			ports.ReconcileOptions{OnlyKart: ports.KartKey(circuit, kart)})
		if err != nil {
			fmt.Fprintf(io.Stderr, "warning: ports: %v\n", err)
			return nil
		}
		summary := summarizeForwards(state.Get(circuit, kart))
		if summary != "" {
			p := style.For(io.Stderr, root.Output == "json")
			if p.Enabled {
				fmt.Fprintln(io.Stderr, p.Dim("forwards: "+summary))
			}
		}
		for _, e := range report.Errors {
			fmt.Fprintf(io.Stderr, "warning: ports: %v\n", e)
		}
		return nil
	}
}

// remapPromptFunc returns true when the user accepts a remap from
// `remote` to `proposed`, false to skip. nil = silent (no prompt; fall
// back to UnionDevcontainer's auto-remap).
type remapPromptFunc func(remote, proposed int) (bool, error)

// unionDevcontainerWithPrompts is the interactive variant of
// ports.UnionDevcontainer. It walks the spec one port at a time:
//
//   - Free ports (no state collision, prober.IsFree) are added at the
//     matching local — no prompt.
//   - Conflicts trigger `prompt(remote, proposed)`:
//     `forward kart's :3000 to local :3001 instead? [Y/n]`. y / enter →
//     AddForward auto-remaps and stamps RemappedFrom. n → skip the
//     port for this session.
//   - When prompt is nil (non-TTY, --output json, tests) the function
//     falls back to ports.UnionDevcontainer's silent auto-remap so CI
//     never blocks on a prompt.
//
// Devcontainer-source entries no longer in spec are pruned up front
// (matches UnionDevcontainer's contract). Explicit / auto entries the
// user added by hand are left alone.
func unionDevcontainerWithPrompts(io IO, state *ports.State, prober ports.LocalProber, circuit, kart string, spec []int, prompt remapPromptFunc) error {
	if prompt == nil {
		_, err := ports.UnionDevcontainer(state, prober, circuit, kart, spec)
		return err
	}

	// Prune devcontainer-source entries that are no longer in spec.
	desired := make(map[int]bool, len(spec))
	for _, p := range spec {
		desired[p] = true
	}
	fwds := state.Get(circuit, kart)
	kept := fwds[:0]
	for _, f := range fwds {
		if f.Source == ports.SourceDevcontainer && !desired[f.Remote] {
			continue
		}
		kept = append(kept, f)
	}
	state.Put(circuit, kart, kept)

	for _, p := range spec {
		// AddForward is idempotent on existing remote entries; if the
		// user has an explicit remap for this port already, leave it.
		if ports.Find(state.Get(circuit, kart), p) >= 0 {
			continue
		}
		taken := state.PortsTaken()
		conflict := taken[p] || !prober.IsFree(p)
		if !conflict {
			if _, err := ports.AddForward(state, prober, circuit, kart, p, 0, ports.SourceDevcontainer); err != nil {
				return err
			}
			continue
		}
		// Compute the same local AddForward will pick so the prompt
		// shows the actual proposal.
		proposed, err := ports.PickFreePort(prober, p+1, taken)
		if err != nil {
			fmt.Fprintf(io.Stderr, "warning: ports: skip %d: %v\n", p, err)
			continue
		}
		ok, err := prompt(p, proposed)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintf(io.Stderr, "skipped :%d (conflict, declined remap)\n", p)
			continue
		}
		if _, err := ports.AddForward(state, prober, circuit, kart, p, 0, ports.SourceDevcontainer); err != nil {
			return err
		}
	}
	return nil
}

// promptRemapConflict renders a y/N confirm with default Y. Returns
// (true, nil) on yes/enter, (false, nil) on no/abort, (false, err)
// only on a real form error.
func promptRemapConflict(remote, proposed int) (bool, error) {
	remap := true
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title(fmt.Sprintf("port :%d is in use on this workstation", remote)).
				Description(fmt.Sprintf("forward kart's :%d to local :%d instead?", remote, proposed)).
				Affirmative(fmt.Sprintf("yes, remap to :%d", proposed)).
				Negative("no, skip this port").
				Value(&remap),
		),
	)
	if err := form.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return false, nil
		}
		return false, err
	}
	return remap, nil
}

// makeAfterExecPortsHook returns the AfterExec callback that tears
// down a kart's ssh forwards when the connect session ends. State.yaml
// entries stay in place — they're the source of truth, the next
// `drift connect` rebinds them. The intent (per plan 15) is "connect
// == lifetime of forwards", so two connections to different karts
// can't accumulate stale local-port bindings.
//
// Best-effort: errors warn on stderr and don't change the connect's
// exit code. `disabled=true` (--no-forwards / --keep-forwards /
// auto_forward_ports=false) returns a no-op hook so the persistent
// behavior of plan 13 is still available.
func makeAfterExecPortsHook(io IO, root *CLI, circuit, kart string, disabled bool) func(context.Context) {
	if disabled {
		return func(context.Context) {}
	}
	return func(ctx context.Context) {
		statePath, err := ports.DefaultPath()
		if err != nil {
			fmt.Fprintf(io.Stderr, "warning: ports teardown: %v\n", err)
			return
		}
		state, err := ports.Load(statePath)
		if err != nil {
			fmt.Fprintf(io.Stderr, "warning: ports teardown: %v\n", err)
			return
		}
		if len(state.Get(circuit, kart)) == 0 {
			return
		}
		driver := ports.NewSSHDriver(driftexec.DefaultRunner)
		// Reconcile a synthetic empty-desired view for this kart only.
		// Cancels every live forward + stops the master, but doesn't
		// touch state.yaml — next connect re-binds from the same
		// entries.
		if err := ports.TeardownKart(ctx, driver, state, circuit, kart); err != nil {
			fmt.Fprintf(io.Stderr, "warning: ports teardown: %v\n", err)
			return
		}
		p := style.For(io.Stderr, root.Output == "json")
		if p.Enabled {
			fmt.Fprintln(io.Stderr, p.Dim("forwards: torn down"))
		}
	}
}

func summarizeForwards(fwds []ports.Forward) string {
	if len(fwds) == 0 {
		return ""
	}
	out := ""
	for i, f := range fwds {
		if i > 0 {
			out += ", "
		}
		if f.RemappedFrom != 0 {
			out += fmt.Sprintf("%d (was %d)", f.Local, f.RemappedFrom)
		} else {
			out += fmt.Sprintf("%d", f.Local)
		}
	}
	return out
}
