package drift

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/charmbracelet/huh"

	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/ports"
	"github.com/kurisu-agent/drift/internal/wire"
)

// portsProbeCmd: `drift ports probe [--kart …]`. Asks lakitu to
// enumerate listening TCP ports inside the kart, then offers a
// huh.MultiSelect for the user to pick which to forward. Off-TTY (or
// `--output json`) it prints the candidate list as JSON instead so
// scripts can consume it.
//
// All of the in-kart logic — devpod ssh, ss invocation, output
// parsing — runs server-side via kart.probe_ports. The client just
// renders the picker and writes the chosen entries to ports.yaml. See
// CLAUDE.md "Client / server boundary" for why we do as little as
// possible on the workstation.
type portsProbeCmd struct {
	Kart string `name:"kart" help:"\"<circuit>/<kart>\" or just \"<kart>\" with -c."`
	All  bool   `name:"all" help:"Skip the picker; add every detected port."`
}

func runPortsProbe(ctx context.Context, io IO, root *CLI, cmd portsProbeCmd, deps deps) int {
	circuit, kart, ok, code := resolvePortsKart(ctx, io, root, deps, cmd.Kart, "drift ports probe")
	if !ok {
		return code
	}

	var res wire.KartProbePortsResult
	if err := deps.call(ctx, circuit, wire.MethodKartProbePorts,
		wire.KartProbePortsParams{Name: kart}, &res); err != nil {
		return errfmt.Emit(io.Stderr, err)
	}

	// 22 is sshd's well-known port — listening inside every kart and
	// never something the user wants to forward; drop it before the
	// picker so the list isn't cluttered. Anything already configured
	// for this kart is also pruned (otherwise probe would offer to
	// re-add a forward that already exists).
	statePath, state, code := loadPortsState(io)
	if state == nil {
		return code
	}
	exclude := map[int]bool{22: true}
	for _, f := range state.Get(circuit, kart) {
		exclude[f.Remote] = true
	}
	candidates := make([]wire.ProbeListener, 0, len(res.Listeners))
	for _, l := range res.Listeners {
		if !exclude[l.Port] {
			candidates = append(candidates, l)
		}
	}

	if len(candidates) == 0 {
		if root.Output == "json" {
			fmt.Fprintln(io.Stdout, "[]")
		} else {
			fmt.Fprintln(io.Stdout, "no new listeners on "+circuit+"/"+kart)
		}
		return 0
	}

	if root.Output == "json" {
		buf, err := json.MarshalIndent(candidates, "", "  ")
		if err != nil {
			return errfmt.Emit(io.Stderr, err)
		}
		fmt.Fprintln(io.Stdout, string(buf))
		return 0
	}

	chosen := candidates
	if !cmd.All {
		picked, ok, err := pickPortsForForward(circuit, kart, candidates)
		if err != nil {
			return errfmt.Emit(io.Stderr, err)
		}
		if !ok || len(picked) == 0 {
			return 0
		}
		chosen = picked
	}

	for _, l := range chosen {
		p := l.Port
		res, err := ports.AddForward(state, ports.DefaultLocalProber, circuit, kart, p, 0, ports.SourceAuto)
		if err != nil {
			fmt.Fprintf(io.Stderr, "warning: skip %d: %v\n", p, err)
			continue
		}
		if res.NoOp {
			continue
		}
		if res.Remapped {
			fmt.Fprintf(io.Stdout, "added %s/%s: %d → :%d (remapped from %d)\n",
				circuit, kart, res.Forward.Local, res.Forward.Remote, res.Forward.RemappedFrom)
		} else {
			fmt.Fprintf(io.Stdout, "added %s/%s: %d → :%d\n",
				circuit, kart, res.Forward.Local, res.Forward.Remote)
		}
	}
	if err := ports.Save(statePath, state); err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	return reconcileForKart(ctx, io, root, deps, circuit, kart)
}

// pickPortsForForward renders a huh.MultiSelect with one row per
// candidate. Each row is `:<port> (<process>)` when the server
// reported a process name, falling back to `:<port>` otherwise.
// Returns the chosen subset; ok=false on user abort (esc / ctrl-c)
// so the caller leaves ports.yaml alone.
func pickPortsForForward(circuit, kart string, candidates []wire.ProbeListener) ([]wire.ProbeListener, bool, error) {
	options := make([]huh.Option[int], len(candidates))
	for i, l := range candidates {
		label := fmt.Sprintf(":%d", l.Port)
		if l.Process != "" {
			label = fmt.Sprintf(":%-5d  %s", l.Port, l.Process)
		}
		options[i] = huh.NewOption(label, i)
	}
	var pickedIdx []int
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewMultiSelect[int]().
				Title(fmt.Sprintf("listeners on %s/%s — pick to forward", circuit, kart)).
				Description("space toggles · enter accepts · esc cancels").
				Options(options...).
				Value(&pickedIdx),
		),
	)
	if err := form.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return nil, false, nil
		}
		return nil, false, err
	}
	out := make([]wire.ProbeListener, 0, len(pickedIdx))
	for _, i := range pickedIdx {
		out = append(out, candidates[i])
	}
	return out, true, nil
}
