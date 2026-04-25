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
