package drift

import (
	"context"
	"fmt"

	"github.com/kurisu-agent/drift/internal/cli/ui"
	driftexec "github.com/kurisu-agent/drift/internal/exec"
	"github.com/kurisu-agent/drift/internal/ports"
)

// makeBeforeExecPortsHook returns the BeforeExec callback `drift connect`
// hands to internal/connect. The hook unions the kart's resolved
// devcontainer forwardPorts into ports.yaml (source=devcontainer),
// reconciles for that kart only, and prints a one-line summary.
//
// Best-effort by design: a failed reconcile warns on stderr but does not
// abort the connect — the user's shell is the primary goal, the
// forwards are a pleasant secondary. `--no-forwards` returns a no-op
// hook so the session stays out of ports.yaml entirely.
func makeBeforeExecPortsHook(io IO, root *CLI, circuit, kart string, noForwards bool) func(context.Context, []int) error {
	if noForwards {
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
			if _, err := ports.UnionDevcontainer(state, ports.DefaultLocalProber, circuit, kart, forwardPorts); err != nil {
				fmt.Fprintf(io.Stderr, "warning: ports: union devcontainer: %v\n", err)
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
			p := ui.NewTheme(io.Stderr, root.Output == "json")
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
