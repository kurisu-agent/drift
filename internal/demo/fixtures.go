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
		CircuitsTotal:     6,
		CircuitsReachable: 5,
		KartsTotal:        18,
		KartsRunning:      12,
		PortsActive:       9,
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
		{Name: "delta", Host: "delta.example.org", Lakitu: "0.4.2", LatencyMS: 91, Reachable: true, Color: "#FF985A"},
		{Name: "prod", Host: "prod.example.org", Lakitu: "0.4.3", LatencyMS: 124, Reachable: true, Color: "#00FFB2"},
		{Name: "edge", Host: "edge.frontier.dev", Lakitu: "", LatencyMS: 0, Reachable: false, Color: "#FF6E63"},
	}, nil
}

func (DataSource) Chest(_ context.Context) ([]dashboard.ResourceRow, error) {
	return []dashboard.ResourceRow{
		{Circuit: "alpha", Name: "OPENAI_API_KEY", Description: "1password://drift/openai", UsedBy: "api, web, plan-14"},
		{Circuit: "alpha", Name: "ANTHROPIC_API_KEY", Description: "1password://drift/anthropic", UsedBy: "api, plan-14"},
		{Circuit: "alpha", Name: "GH_TOKEN", Description: "env:GITHUB_TOKEN", UsedBy: "api, web"},
		{Circuit: "alpha", Name: "STRIPE_SECRET_KEY", Description: "1password://drift/stripe", UsedBy: "api"},
		{Circuit: "alpha", Name: "DATABASE_URL", Description: "literal:postgres://…/alpha", UsedBy: "api, db"},
		{Circuit: "beta", Name: "ANTHROPIC_API_KEY", Description: "1password://drift/anthropic", UsedBy: "experiments"},
		{Circuit: "beta", Name: "HF_TOKEN", Description: "1password://drift/huggingface", UsedBy: "experiments, scratch"},
		{Circuit: "beta", Name: "WANDB_API_KEY", Description: "env:WANDB_KEY", UsedBy: "experiments"},
		{Circuit: "gamma", Name: "AWS_ACCESS_KEY_ID", Description: "aws-vault:training", UsedBy: "training, eval"},
		{Circuit: "gamma", Name: "AWS_SECRET_ACCESS_KEY", Description: "aws-vault:training", UsedBy: "training, eval"},
		{Circuit: "gamma", Name: "S3_BUCKET", Description: "literal:gamma-research-data", UsedBy: "training, eval"},
		{Circuit: "prod", Name: "DATADOG_API_KEY", Description: "1password://drift/datadog-prod", UsedBy: "ingester, web-prod"},
		{Circuit: "prod", Name: "PAGERDUTY_TOKEN", Description: "1password://drift/pagerduty", UsedBy: "ingester"},
	}, nil
}

func (DataSource) Characters(_ context.Context) ([]dashboard.ResourceRow, error) {
	return []dashboard.ResourceRow{
		{Circuit: "alpha", Name: "alice", Description: "alice@example.org · alice", UsedBy: "api, web, plan-14"},
		{Circuit: "alpha", Name: "ops", Description: "ops@example.org · drift-ops", UsedBy: "db"},
		{Circuit: "alpha", Name: "ci-bot", Description: "ci@example.org · drift-ci-bot", UsedBy: "api"},
		{Circuit: "beta", Name: "alice", Description: "alice@example.org · alice", UsedBy: "experiments, scratch"},
		{Circuit: "beta", Name: "research", Description: "research@example.org · drift-research", UsedBy: "experiments"},
		{Circuit: "gamma", Name: "training", Description: "ml@example.org · drift-train", UsedBy: "training, eval"},
		{Circuit: "gamma", Name: "ops", Description: "ops@example.org · drift-ops", UsedBy: "old-job"},
		{Circuit: "delta", Name: "alice", Description: "alice@example.org · alice", UsedBy: "edge-proxy"},
		{Circuit: "prod", Name: "deploy", Description: "deploy@example.org · drift-deploy", UsedBy: "ingester, web-prod"},
		{Circuit: "prod", Name: "oncall", Description: "oncall@example.org · drift-oncall", UsedBy: "ingester"},
	}, nil
}

func (DataSource) Tunes(_ context.Context) ([]dashboard.ResourceRow, error) {
	return []dashboard.ResourceRow{
		{Circuit: "alpha", Name: "go-stack", Description: "ghcr.io/example/go:1.26 · go,git,gh,nodejs", UsedBy: "api, plan-14"},
		{Circuit: "alpha", Name: "ts-stack", Description: "ghcr.io/example/node:22 · pnpm,git,gh", UsedBy: "web"},
		{Circuit: "alpha", Name: "py-stack", Description: "ghcr.io/example/python:3.13 · uv,git", UsedBy: "db"},
		{Circuit: "alpha", Name: "rust-stack", Description: "ghcr.io/example/rust:1.84 · cargo,git", UsedBy: "(unused)"},
		{Circuit: "beta", Name: "py-research", Description: "ghcr.io/example/python:3.13-cuda · uv,torch", UsedBy: "experiments, scratch"},
		{Circuit: "beta", Name: "py-stack", Description: "ghcr.io/example/python:3.13 · uv,git", UsedBy: "(unused)"},
		{Circuit: "gamma", Name: "py-train", Description: "ghcr.io/example/python:3.13-cuda12 · uv,torch,wandb", UsedBy: "training, eval"},
		{Circuit: "gamma", Name: "py-stack", Description: "ghcr.io/example/python:3.13 · uv,git", UsedBy: "old-job"},
		{Circuit: "delta", Name: "edge-go", Description: "ghcr.io/example/go-tinygo:0.32 · tinygo,git", UsedBy: "edge-proxy"},
		{Circuit: "prod", Name: "go-prod", Description: "ghcr.io/example/go:1.26-distroless · go,git", UsedBy: "ingester, web-prod"},
	}, nil
}

func (DataSource) Ports(_ context.Context) ([]dashboard.PortRow, error) {
	return []dashboard.PortRow{
		{Local: 3000, Remote: 3000, Circuit: "alpha", Kart: "web", Active: true},
		{Local: 8080, Remote: 8080, Circuit: "alpha", Kart: "api", Active: true},
		{Local: 5432, Remote: 5432, Circuit: "alpha", Kart: "db", Active: false},
		{Local: 6379, Remote: 6379, Circuit: "alpha", Kart: "api", Active: true},
		{Local: 4173, Remote: 4173, Circuit: "alpha", Kart: "plan-14", Active: true},
		{Local: 9090, Remote: 9090, Circuit: "beta", Kart: "experiments", Active: true},
		{Local: 7860, Remote: 7860, Circuit: "beta", Kart: "experiments", Active: true},
		{Local: 8888, Remote: 8888, Circuit: "beta", Kart: "scratch", Active: false},
		{Local: 6006, Remote: 6006, Circuit: "gamma", Kart: "training", Active: true},
		{Local: 8265, Remote: 8265, Circuit: "gamma", Kart: "eval", Active: true},
		{Local: 4000, Remote: 4000, Circuit: "delta", Kart: "edge-proxy", Active: true},
		{Local: 9100, Remote: 9100, Circuit: "prod", Kart: "ingester", Active: false},
	}, nil
}

func karts() []dashboard.KartRow {
	return []dashboard.KartRow{
		{Circuit: "alpha", Name: "api", Status: "running", Source: "clone", Tune: "go-stack", Autostart: true},
		{Circuit: "alpha", Name: "web", Status: "running", Source: "clone", Tune: "ts-stack"},
		{Circuit: "alpha", Name: "db", Status: "stopped", Source: "starter", Tune: "py-stack"},
		{Circuit: "alpha", Name: "plan-14", Status: "running", Source: "clone", Tune: "go-stack"},
		{Circuit: "alpha", Name: "scripts", Status: "stopped", Source: "starter", Tune: "py-stack"},
		{Circuit: "beta", Name: "experiments", Status: "running", Source: "clone", Tune: "py-research", Autostart: true},
		{Circuit: "beta", Name: "scratch", Status: "stale", Source: "none", Tune: "py-research"},
		{Circuit: "beta", Name: "datasets", Status: "running", Source: "clone", Tune: "py-research"},
		{Circuit: "gamma", Name: "training", Status: "running", Source: "clone", Tune: "py-train", Autostart: true},
		{Circuit: "gamma", Name: "eval", Status: "running", Source: "clone", Tune: "py-train"},
		{Circuit: "gamma", Name: "old-job", Status: "stopped", Source: "starter", Tune: "py-stack"},
		{Circuit: "gamma", Name: "scratch", Status: "running", Source: "none", Tune: "py-train"},
		{Circuit: "delta", Name: "edge-proxy", Status: "running", Source: "clone", Tune: "edge-go", Autostart: true},
		{Circuit: "delta", Name: "edge-cache", Status: "stale", Source: "clone", Tune: "edge-go"},
		{Circuit: "prod", Name: "ingester", Status: "running", Source: "clone", Tune: "go-prod", Autostart: true},
		{Circuit: "prod", Name: "web-prod", Status: "running", Source: "clone", Tune: "go-prod", Autostart: true},
		{Circuit: "prod", Name: "migrator", Status: "stopped", Source: "starter", Tune: "go-prod"},
		{Circuit: "edge", Name: "frontier", Status: "unreachable", Source: "clone", Tune: "edge-go"},
	}
}

func activity() []dashboard.ActivityEntry {
	// Use real time.Now rather than Now() so the "Nm ago" relative
	// labels are accurate at the moment a frame is rendered. The
	// status panel's snapshot test uses its own fixture (fixtureDS in
	// dashboard) so this doesn't make those tests non-deterministic.
	now := time.Now()
	return []dashboard.ActivityEntry{
		{When: now.Add(-3 * time.Minute), Action: "drift new", Kart: "alpha.plan-14", Detail: "from example-org/template"},
		{When: now.Add(-12 * time.Minute), Action: "kart restart", Kart: "alpha.api"},
		{When: now.Add(-21 * time.Minute), Action: "port add", Kart: "alpha.web", Detail: ":3000 -> :3000"},
		{When: now.Add(-34 * time.Minute), Action: "kart info", Kart: "beta.experiments"},
		{When: now.Add(-58 * time.Minute), Action: "drift connect", Kart: "alpha.api"},
		{When: now.Add(-1 * time.Hour), Action: "drift status", Detail: "6 circuits · 12 running"},
		{When: now.Add(-2 * time.Hour), Action: "kart stop", Kart: "alpha.experiments"},
		{When: now.Add(-3 * time.Hour), Action: "tune update", Kart: "gamma.training", Detail: "py-train v3.2.0 → v3.2.1"},
		{When: now.Add(-4 * time.Hour), Action: "circuit add", Detail: "edge ← edge.frontier.dev"},
		{When: now.Add(-5 * time.Hour), Action: "kart rebuild", Kart: "prod.ingester"},
		{When: now.Add(-6 * time.Hour), Action: "chest add", Kart: "prod", Detail: "DATADOG_API_KEY"},
		{When: now.Add(-8 * time.Hour), Action: "kart restart", Kart: "delta.edge-proxy"},
	}
}
