package drift

import (
	"context"
	"errors"
	"fmt"
	"io"

	"charm.land/huh/v2"
	"github.com/kurisu-agent/drift/internal/wire"
)

// driftCheckResult mirrors server.DriftCheckResult without dragging
// the whole server package into the drift client. JSON tags match
// kart.drift_check's wire response.
type driftCheckResult struct {
	Name    string         `json:"name"`
	Drifted bool           `json:"drifted"`
	Fields  []driftedField `json:"fields,omitempty"`
}

type driftedField struct {
	Source     string `json:"source"`
	SourceName string `json:"source_name,omitempty"`
	Path       string `json:"path"`
	Was        any    `json:"was,omitempty"`
	Now        any    `json:"now,omitempty"`
}

// maybePromptRebuild runs the kart.drift_check preamble for a
// connect. On drift with a TTY stdin it prints a summary and asks
// `rebuild now? [y/N]` — no means connect as usual, yes calls
// kart.rebuild before the connect. Non-TTY or json output: print a
// one-line warning and skip the prompt so scripts never block.
//
// Fail-soft: if the RPC itself errors (stale lakitu without the
// method, transient transport hiccup, anything), log a warning to
// stderr and return nil so the connect still proceeds.
func maybePromptRebuild(ctx context.Context, stderr io.Writer, stdinTTY, stdoutTTY, jsonOut bool, d deps, circuit, kart string) error {
	var res driftCheckResult
	if err := d.call(ctx, circuit, wire.MethodKartDriftCheck, map[string]any{"name": kart}, &res); err != nil {
		// Soft fail — don't block a connect on a diagnostic probe.
		fmt.Fprintf(stderr, "warning: drift check failed: %v\n", err)
		return nil
	}
	if !res.Drifted {
		return nil
	}

	interactive := stdinTTY && stdoutTTY && !jsonOut
	if !interactive {
		// Non-interactive: surface drift and let the user decide.
		fmt.Fprintf(stderr, "warning: kart %q has drifted from tune — run `drift kart rebuild %s` to apply\n", kart, kart)
		return nil
	}

	fmt.Fprintf(stderr, "kart %q has drifted from %s %q:\n", kart, res.Fields[0].Source, res.Fields[0].SourceName)
	for _, f := range res.Fields {
		fmt.Fprintf(stderr, "  %s: %v → %v\n", f.Path, f.Was, f.Now)
	}

	var rebuild bool
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title("rebuild now?").
				Description("applies tune changes by recreating the container (destroys in-container state)").
				Affirmative("yes, rebuild").
				Negative("no, just connect").
				Value(&rebuild),
		),
	)
	if err := form.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return nil
		}
		return err
	}
	if !rebuild {
		return nil
	}
	fmt.Fprintln(stderr, "rebuilding…")
	var rr map[string]any
	if err := d.call(ctx, circuit, wire.MethodKartRebuild, map[string]any{"name": kart}, &rr); err != nil {
		return fmt.Errorf("kart.rebuild: %w", err)
	}
	fmt.Fprintln(stderr, "rebuild complete")
	return nil
}
