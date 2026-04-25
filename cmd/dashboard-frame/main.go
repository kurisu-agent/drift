// Command dashboard-frame renders one settled frame of the drift
// dashboard against demo fixtures and writes the colored ANSI text to
// stdout. Pipe it into `freeze` (charm.land/freeze) to produce a PNG
// still — that's the visual eval loop the redesign plan calls for.
//
// Usage:
//
//	dashboard-frame [-tab status|karts|...] [-w 120] [-h 30] >frame.ans
//	dashboard-frame | freeze --output dashboard.png
//
// The renderer drives the model synchronously: Init's data fetches are
// drained, the WindowSizeMsg is delivered, and the entrance animation
// is stepped to its settled state before View() is captured. No real
// terminal, no real-time, deterministic by design.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/kurisu-agent/drift/internal/cli/ui"
	"github.com/kurisu-agent/drift/internal/cli/ui/dashboard"
	"github.com/kurisu-agent/drift/internal/demo"
)

func main() {
	tab := flag.String("tab", "status", "initial tab (status|karts|circuits|chest|characters|tunes|ports|logs|cross-cut)")
	scenarioName := flag.String("scenario", "default", "scenario name (see scenarios.go)")
	width := flag.Int("w", 120, "frame width in columns")
	height := flag.Int("h", 30, "frame height in rows")
	noMotion := flag.Bool("no-motion", false, "skip the entrance animation; render the settled frame directly")
	at := flag.Duration("at", -1, "render at simulated time `at` from entrance start (e.g. 100ms); negative = settled, 0 = first frame")
	flag.Parse()

	sc, err := findScenario(*scenarioName)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	t, err := parseTab(*tab)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	// Force-enable the theme. ui.NewTheme inspects the writer for TTY-
	// ness; here stdout is a pipe (freeze) so we'd otherwise get a
	// no-op palette. Probe through ui.NewTheme on stderr to pick up
	// the real terminal's dark/light + colorprofile detection, then
	// flip Enabled on so the styles render to ANSI regardless.
	probe := ui.NewTheme(os.Stderr, false)
	theme := *probe
	theme.Enabled = true

	opts := dashboard.Options{
		InitialTab:     t,
		Theme:          &theme,
		DriftVersion:   "0.4.3",
		DataSource:     demo.New(),
		MotionDisabled: *noMotion,
	}
	sc.apply(&opts, width, height)
	var frame string
	if *at >= 0 && !*noMotion {
		frame = dashboard.RenderFrameAt(opts, *width, *height, *at)
	} else {
		frame = dashboard.RenderSettledFrame(opts, *width, *height)
	}
	if _, err := os.Stdout.WriteString(frame); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func parseTab(s string) (dashboard.Tab, error) {
	switch s {
	case "status", "cross-cut":
		// cross-cut is a pseudo-tab used for chrome scenarios (palette,
		// help modal, toasts, narrow widths). It always lands on the
		// status tab so the welded strip and outer border are visible
		// while the scenario function layers its overlay on top.
		return dashboard.TabStatus, nil
	case "karts":
		return dashboard.TabKarts, nil
	case "circuits":
		return dashboard.TabCircuits, nil
	case "chest":
		return dashboard.TabChest, nil
	case "characters":
		return dashboard.TabCharacters, nil
	case "tunes":
		return dashboard.TabTunes, nil
	case "ports":
		return dashboard.TabPorts, nil
	case "logs":
		return dashboard.TabLogs, nil
	}
	return 0, fmt.Errorf("unknown tab %q", s)
}
