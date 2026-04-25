package main

import (
	"fmt"

	"github.com/kurisu-agent/drift/internal/cli/ui/dashboard"
)

// scenario is the small contract a frame variant satisfies. plan-16's
// rubric drives a per-(tab, scenario) PNG matrix; each scenario
// mutates the dashboard Options and the requested viewport before the
// settled frame is captured. Add new scenarios as the underlying panel
// features land — filter, row-expand, palette, etc. live behind real
// runtime state, so the scenario function will pre-drive key sequences
// once those panels learn to receive them.
type scenario struct {
	name        string
	tab         string // empty = applies to any tab; "cross-cut" = pseudo-tab for chrome scenarios
	description string
	apply       func(opts *dashboard.Options, width, height *int)
}

// scenarios registry. Names follow plan-16 lines 76-87. Order matters
// only for the Makefile loop; per-tab `default` is the implicit
// fallback when no scenario is supplied.
var scenarios = []scenario{
	{
		name:        "default",
		description: "settled frame against the demo fixture for whatever tab is requested",
		apply:       func(*dashboard.Options, *int, *int) {},
	},
	{
		name:        "narrow-80c",
		tab:         "cross-cut",
		description: "render the dashboard at 80 columns to verify the welded tab strip and tables degrade gracefully",
		apply: func(_ *dashboard.Options, w, _ *int) {
			*w = 80
		},
	},
	{
		name:        "filter-active",
		tab:         "karts",
		description: "karts panel with a filter pre-applied; non-matching rows render dim and the match-count strip is visible",
		apply: func(o *dashboard.Options, _, _ *int) {
			o.InitialFilter = "alpha"
		},
	},
	{
		name:        "with-color-tint",
		tab:         "circuits",
		description: "dashboard anchored to a single circuit; outer chrome re-tints to the per-circuit accent (alpha = #6B50FF here, but the override wires through theme.WithAccent)",
		apply: func(o *dashboard.Options, _, _ *int) {
			// Anchor to alpha and use a contrasting tint so the
			// override is visibly different from the default Charple
			// brand accent.
			o.CircuitFilter = "alpha"
			o.AccentOverride = "#FF388B" // charmtone.Cherry
		},
	},
}

// findScenario resolves a scenario by name, falling back to default
// when name is empty. Unknown scenarios are an error so the Makefile
// can fail loudly rather than silently rendering a default frame.
func findScenario(name string) (scenario, error) {
	if name == "" {
		name = "default"
	}
	for _, s := range scenarios {
		if s.name == name {
			return s, nil
		}
	}
	return scenario{}, fmt.Errorf("unknown scenario %q", name)
}
