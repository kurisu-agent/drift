package drift

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/charmbracelet/huh"

	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/ports"
	"github.com/kurisu-agent/drift/internal/wire"
)

// portsProbeCmd: `drift ports probe [--kart …]`. Asks lakitu for a
// kart's forwardable ports — the union of live `ss -tlnpH` listeners
// and the static `forwardPorts` from devcontainer.json — then offers a
// huh.MultiSelect for the user to pick which to forward. Off-TTY (or
// `--output json`) it prints the candidate list as JSON instead so
// scripts can consume it.
//
// All of the in-kart logic — devpod ssh, ss invocation, devcontainer
// parsing — runs server-side via kart.probe_ports. The client just
// renders the picker and writes the chosen entries to ports.yaml. See
// CLAUDE.md "Client / server boundary" for why we do as little as
// possible on the workstation.
type portsProbeCmd struct {
	Kart string `name:"kart" help:"\"<circuit>/<kart>\" or just \"<kart>\" with -c."`
	All  bool   `name:"all" help:"Skip the picker; add every detected port."`
}

// probeCandidate is the merged view of one port the picker offers. A
// port may be backed by a live listener (Process set), a devcontainer
// forwardPorts entry (Devcontainer=true), or both — process-name wins
// for the label since it's strictly more informative.
type probeCandidate struct {
	Port         int    `json:"port"`
	Process      string `json:"process,omitempty"`
	Devcontainer bool   `json:"devcontainer,omitempty"`
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
	candidates := mergeProbeCandidates(res.Listeners, res.DevcontainerPorts, exclude)

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

	for _, c := range chosen {
		// Pure-devcontainer entries (no live listener) get
		// Source=devcontainer so the next `drift connect` passthrough
		// recognises them as already accounted for and doesn't double
		// up. Anything seen by ss is recorded as Auto — that's the
		// "user explicitly picked this listener" path.
		source := ports.SourceAuto
		if c.Devcontainer && c.Process == "" {
			source = ports.SourceDevcontainer
		}
		addRes, err := ports.AddForward(state, ports.DefaultLocalProber, circuit, kart, c.Port, 0, source)
		if err != nil {
			fmt.Fprintf(io.Stderr, "warning: skip %d: %v\n", c.Port, err)
			continue
		}
		if addRes.NoOp {
			continue
		}
		if addRes.Remapped {
			fmt.Fprintf(io.Stdout, "added %s/%s: %d → :%d (remapped from %d)\n",
				circuit, kart, addRes.Forward.Local, addRes.Forward.Remote, addRes.Forward.RemappedFrom)
		} else {
			fmt.Fprintf(io.Stdout, "added %s/%s: %d → :%d\n",
				circuit, kart, addRes.Forward.Local, addRes.Forward.Remote)
		}
	}
	if err := ports.Save(statePath, state); err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	return reconcileForKart(ctx, io, root, deps, circuit, kart)
}

// mergeProbeCandidates folds the two server-reported lists into one
// deduplicated, sorted set. A port that appears in both keeps the
// listener's process name (strictly more useful in the picker) and
// gets Devcontainer=true so the picker can still hint that the kart
// declared it. Excluded ports (port 22, already-configured) are
// dropped before the merge.
func mergeProbeCandidates(listeners []wire.ProbeListener, devcontainerPorts []int, exclude map[int]bool) []probeCandidate {
	by := make(map[int]*probeCandidate)
	for _, l := range listeners {
		if exclude[l.Port] {
			continue
		}
		by[l.Port] = &probeCandidate{Port: l.Port, Process: l.Process}
	}
	for _, p := range devcontainerPorts {
		if exclude[p] {
			continue
		}
		if c, ok := by[p]; ok {
			c.Devcontainer = true
			continue
		}
		by[p] = &probeCandidate{Port: p, Devcontainer: true}
	}
	out := make([]probeCandidate, 0, len(by))
	for _, c := range by {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	if len(out) == 0 {
		return nil
	}
	return out
}

// pickPortsForForward renders a huh.MultiSelect with one row per
// candidate. Each row is `:<port>  <process>` when the server reported
// a process name, `:<port>  (devcontainer)` when the port came purely
// from devcontainer.json, and `:<port>` otherwise. Returns the chosen
// subset; ok=false on user abort (esc / ctrl-c) so the caller leaves
// ports.yaml alone.
func pickPortsForForward(circuit, kart string, candidates []probeCandidate) ([]probeCandidate, bool, error) {
	options := make([]huh.Option[int], len(candidates))
	for i, c := range candidates {
		label := fmt.Sprintf(":%d", c.Port)
		switch {
		case c.Process != "":
			label = fmt.Sprintf(":%-5d  %s", c.Port, c.Process)
		case c.Devcontainer:
			label = fmt.Sprintf(":%-5d  (devcontainer)", c.Port)
		}
		options[i] = huh.NewOption(label, i)
	}
	var pickedIdx []int
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewMultiSelect[int]().
				Title(fmt.Sprintf("ports on %s/%s — pick to forward", circuit, kart)).
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
	out := make([]probeCandidate, 0, len(pickedIdx))
	for _, i := range pickedIdx {
		out = append(out, candidates[i])
	}
	return out, true, nil
}
