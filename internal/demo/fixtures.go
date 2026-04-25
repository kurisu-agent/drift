// Package demo provides fixture data shared by `drift dashboard --demo`
// and the integration tests, so the README GIF and the regression suite
// can never disagree about what drift's world looks like.
package demo

import (
	"context"
	"time"

	"github.com/kurisu-agent/drift/internal/cli/ui/dashboard"
)

// Now is the fixed clock the fixtures pretend it is. Keeps activity-table
// "Nm ago" relative timestamps deterministic across runs.
func Now() time.Time {
	return time.Date(2026, 4, 25, 14, 0, 0, 0, time.UTC)
}

// DataSource fulfils dashboard.DataSource against the canned fixtures.
type DataSource struct{}

// New returns a stateless fixture data source.
func New() dashboard.DataSource { return &DataSource{} }

func (DataSource) Status(_ context.Context) (dashboard.StatusSnapshot, error) {
	return dashboard.StatusSnapshot{
		DriftVersion:      "0.4.3",
		CircuitsTotal:     3,
		CircuitsReachable: 3,
		KartsTotal:        9,
		KartsRunning:      7,
		PortsActive:       4,
		Activity:          activity(),
	}, nil
}

func (DataSource) Karts(_ context.Context, circuit string) ([]dashboard.KartRow, error) {
	rows := karts()
	if circuit == "" {
		return rows, nil
	}
	out := rows[:0]
	for _, r := range rows {
		if r.Circuit == circuit {
			out = append(out, r)
		}
	}
	return out, nil
}

func (DataSource) Circuits(_ context.Context) ([]dashboard.CircuitRow, error) {
	return []dashboard.CircuitRow{
		{Name: "alpha", Host: "alpha.example.org", Default: true, Lakitu: "0.4.3", LatencyMS: 18, Reachable: true, Color: "#6B50FF"},
		{Name: "beta", Host: "beta.example.org", Lakitu: "0.4.3", LatencyMS: 42, Reachable: true, Color: "#0ADCD9"},
		{Name: "gamma", Host: "gamma.example.org", Lakitu: "0.4.3", LatencyMS: 67, Reachable: true, Color: "#FF388B"},
	}, nil
}

func (DataSource) Chest(_ context.Context) ([]dashboard.ResourceRow, error) {
	return []dashboard.ResourceRow{
		{Circuit: "alpha", Name: "OPENAI_API_KEY", Description: "1password://drift/openai", UsedBy: "api, web"},
		{Circuit: "alpha", Name: "GH_TOKEN", Description: "env:GITHUB_TOKEN", UsedBy: "api"},
		{Circuit: "beta", Name: "ANTHROPIC_API_KEY", Description: "1password://drift/anthropic", UsedBy: "experiments"},
	}, nil
}

func (DataSource) Characters(_ context.Context) ([]dashboard.ResourceRow, error) {
	return []dashboard.ResourceRow{
		{Circuit: "alpha", Name: "alice", Description: "alice@example.org · alice", UsedBy: "api, web"},
		{Circuit: "alpha", Name: "ops", Description: "ops@example.org · drift-ops", UsedBy: "experiments"},
		{Circuit: "beta", Name: "alice", Description: "alice@example.org · alice", UsedBy: "experiments"},
	}, nil
}

func (DataSource) Tunes(_ context.Context) ([]dashboard.ResourceRow, error) {
	return []dashboard.ResourceRow{
		{Circuit: "alpha", Name: "go-stack", Description: "ghcr.io/example/go:1.26 · 4 features", UsedBy: "api"},
		{Circuit: "alpha", Name: "ts-stack", Description: "ghcr.io/example/node:22 · 3 features", UsedBy: "web"},
		{Circuit: "beta", Name: "py-stack", Description: "ghcr.io/example/python:3.13 · 2 features", UsedBy: "experiments"},
	}, nil
}

func (DataSource) Ports(_ context.Context) ([]dashboard.PortRow, error) {
	return []dashboard.PortRow{
		{Local: 3000, Remote: 3000, Circuit: "alpha", Kart: "web", Active: true},
		{Local: 8080, Remote: 8080, Circuit: "alpha", Kart: "api", Active: true},
		{Local: 5432, Remote: 5432, Circuit: "alpha", Kart: "db", Active: false},
		{Local: 9090, Remote: 9090, Circuit: "beta", Kart: "experiments", Active: true},
	}, nil
}

func karts() []dashboard.KartRow {
	return []dashboard.KartRow{
		{Circuit: "alpha", Name: "api", Status: "running", Source: "clone", Tune: "go-stack", Autostart: true},
		{Circuit: "alpha", Name: "web", Status: "running", Source: "clone", Tune: "ts-stack"},
		{Circuit: "alpha", Name: "db", Status: "stopped", Source: "starter", Tune: "py-stack"},
		{Circuit: "alpha", Name: "plan-14", Status: "running", Source: "clone", Tune: "go-stack"},
		{Circuit: "beta", Name: "experiments", Status: "running", Source: "clone", Tune: "py-stack", Autostart: true},
		{Circuit: "beta", Name: "scratch", Status: "stale", Source: "none"},
		{Circuit: "gamma", Name: "training", Status: "running", Source: "clone", Tune: "py-stack"},
		{Circuit: "gamma", Name: "eval", Status: "running", Source: "clone", Tune: "py-stack"},
		{Circuit: "gamma", Name: "old-job", Status: "stopped", Source: "starter"},
	}
}

func activity() []dashboard.ActivityEntry {
	now := Now()
	return []dashboard.ActivityEntry{
		{When: now.Add(-3 * time.Minute), Action: "drift new", Kart: "alpha.plan-14", Detail: "from example-org/template"},
		{When: now.Add(-12 * time.Minute), Action: "kart restart", Kart: "alpha.api"},
		{When: now.Add(-1 * time.Hour), Action: "port add", Kart: "alpha.web", Detail: ":3000 -> :3000"},
		{When: now.Add(-2 * time.Hour), Action: "drift status", Kart: "", Detail: ""},
		{When: now.Add(-3 * time.Hour), Action: "kart info", Kart: "beta.experiments"},
		{When: now.Add(-4 * time.Hour), Action: "drift connect", Kart: "alpha.api"},
		{When: now.Add(-5 * time.Hour), Action: "kart stop", Kart: "alpha.experiments"},
	}
}
