package main

import (
	"context"
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
	{
		name:        "with-conflict",
		tab:         "ports",
		description: "ports panel with two rows binding the same workstation port; warn pill + leading warning glyph mark the conflict",
		apply: func(o *dashboard.Options, _, _ *int) {
			o.DataSource = withPortConflict{base: o.DataSource}
		},
	},
	{
		name:        "follow-on",
		tab:         "logs",
		description: "logs panel with follow=true; '● follow' badge sits flush right in the brand accent",
		apply: func(o *dashboard.Options, _, _ *int) {
			t := true
			o.LogsFollowDefault = &t
		},
	},
	{
		name:        "filter-active",
		tab:         "logs",
		description: "logs panel with a filter pre-applied; non-matching lines render dim, the match-count strip is visible",
		apply: func(o *dashboard.Options, _, _ *int) {
			o.InitialFilter = "auth"
			f := false
			o.LogsFollowDefault = &f
		},
	},
}

// withPortConflict wraps a DataSource and injects an extra port row
// that conflicts with an existing local port, so the with-conflict
// scenario can capture the warn-pill rendering without the live
// fixture needing a hand-curated dup.
type withPortConflict struct {
	base dashboard.DataSource
}

func (w withPortConflict) Status(ctx context.Context) (dashboard.StatusSnapshot, error) {
	return w.base.Status(ctx)
}
func (w withPortConflict) Karts(ctx context.Context, c string) ([]dashboard.KartRow, error) {
	return w.base.Karts(ctx, c)
}
func (w withPortConflict) Circuits(ctx context.Context) ([]dashboard.CircuitRow, error) {
	return w.base.Circuits(ctx)
}
func (w withPortConflict) Chest(ctx context.Context) ([]dashboard.ResourceRow, error) {
	return w.base.Chest(ctx)
}
func (w withPortConflict) Characters(ctx context.Context) ([]dashboard.ResourceRow, error) {
	return w.base.Characters(ctx)
}
func (w withPortConflict) Tunes(ctx context.Context) ([]dashboard.ResourceRow, error) {
	return w.base.Tunes(ctx)
}
func (w withPortConflict) Ports(ctx context.Context) ([]dashboard.PortRow, error) {
	rows, err := w.base.Ports(ctx)
	if err != nil {
		return rows, err
	}
	// Stub a second forward bound to local 3000 (already used by
	// alpha.web in the demo fixture) so two rows reach for the same
	// host port and trip the conflict detector.
	rows = append(rows,
		dashboard.PortRow{Local: 3000, Remote: 4000, Circuit: "beta", Kart: "experiments", Active: true},
	)
	return rows, nil
}

// findScenario resolves a scenario by (name, tab) with tab acting as
// disambiguation when two panels both define a scenario with the
// same short name (e.g. "filter-active" exists for karts and logs).
// Unknown scenarios are an error so the Makefile can fail loudly
// rather than silently rendering a default frame.
func findScenario(name, tab string) (scenario, error) {
	if name == "" {
		name = "default"
	}
	// First pass: exact name+tab match.
	for _, s := range scenarios {
		if s.name == name && s.tab == tab {
			return s, nil
		}
	}
	// Second pass: name match where the scenario applies to any tab
	// (s.tab == "") — covers "default", which is shared.
	for _, s := range scenarios {
		if s.name == name && s.tab == "" {
			return s, nil
		}
	}
	// Third pass: name match regardless of tab — covers cross-cut
	// chrome scenarios that name a pseudo-tab the caller may have
	// translated already.
	for _, s := range scenarios {
		if s.name == name {
			return s, nil
		}
	}
	return scenario{}, fmt.Errorf("unknown scenario %q for tab %q", name, tab)
}
