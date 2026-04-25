package drift

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/cli/ui"
	"github.com/kurisu-agent/drift/internal/cli/ui/dashboard"
	"github.com/kurisu-agent/drift/internal/config"
	"github.com/kurisu-agent/drift/internal/demo"
	"github.com/kurisu-agent/drift/internal/version"
)

type dashboardCmd struct {
	Tab  string `name:"tab" help:"Initial tab (status|karts|circuits|chest|characters|tunes|ports|logs)."`
	Demo bool   `name:"demo" hidden:"" help:"Render against canned fixtures instead of live RPCs (used for the README GIF)."`
}

func runDashboard(ctx context.Context, io IO, root *CLI, cmd dashboardCmd, deps deps) int {
	if !stdinIsTTY(io.Stdin) || !stdoutIsTTY(io.Stdout) {
		fmt.Fprintln(io.Stderr, "drift dashboard requires a TTY (try `drift status` or `drift karts`)")
		return 1
	}
	tab, err := parseDashboardTab(cmd.Tab)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	t := ui.NewTheme(io.Stdout, false)
	var ds dashboard.DataSource
	if cmd.Demo || os.Getenv("DRIFT_DEMO") != "" {
		ds = demo.New()
	} else {
		ds = newLiveDataSource(deps, root)
	}
	opts := dashboard.Options{
		InitialTab:    tab,
		CircuitFilter: root.Circuit,
		Theme:         t,
		Demo:          cmd.Demo,
		DriftVersion:  version.Get().Version,
		DataSource:    ds,
	}
	if err := dashboard.Run(ctx, io.Stdin, io.Stdout, opts); err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	return 0
}

func parseDashboardTab(s string) (dashboard.Tab, error) {
	switch s {
	case "", "status":
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

// liveDataSource fulfils dashboard.DataSource against the same deps the
// CLI commands use (status probe, kart list RPC, circuit config, ...).
type liveDataSource struct {
	deps deps
	root *CLI
}

func newLiveDataSource(d deps, r *CLI) dashboard.DataSource {
	return &liveDataSource{deps: d, root: r}
}

func (l *liveDataSource) Status(ctx context.Context) (dashboard.StatusSnapshot, error) {
	cfgPath, err := l.deps.clientConfigPath()
	if err != nil {
		return dashboard.StatusSnapshot{}, err
	}
	cfg, err := config.LoadClient(cfgPath)
	if err != nil {
		return dashboard.StatusSnapshot{}, err
	}
	circuits, _ := collectCircuitKarts(ctx, cfg, l.deps)
	snap := dashboard.StatusSnapshot{
		DriftVersion:      version.Get().Version,
		CircuitsTotal:     len(cfg.Circuits),
		CircuitsReachable: 0,
	}
	seenCircuits := map[string]bool{}
	for _, k := range circuits {
		seenCircuits[k.Circuit] = true
		snap.KartsTotal++
		if k.Entry.Status == "running" {
			snap.KartsRunning++
		}
	}
	snap.CircuitsReachable = len(seenCircuits)
	return snap, nil
}

func (l *liveDataSource) Karts(ctx context.Context, _ string) ([]dashboard.KartRow, error) {
	cfgPath, err := l.deps.clientConfigPath()
	if err != nil {
		return nil, err
	}
	cfg, err := config.LoadClient(cfgPath)
	if err != nil {
		return nil, err
	}
	karts, _ := collectCircuitKarts(ctx, cfg, l.deps)
	rows := make([]dashboard.KartRow, 0, len(karts))
	for _, k := range karts {
		rows = append(rows, dashboard.KartRow{
			Circuit:   k.Circuit,
			Name:      k.Entry.Name,
			Status:    k.Entry.Status,
			Source:    k.Entry.Source.Mode,
			Tune:      k.Entry.Tune,
			Autostart: k.Entry.Autostart,
		})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Circuit != rows[j].Circuit {
			return rows[i].Circuit < rows[j].Circuit
		}
		return rows[i].Name < rows[j].Name
	})
	return rows, nil
}

func (l *liveDataSource) Circuits(ctx context.Context) ([]dashboard.CircuitRow, error) {
	cfgPath, err := l.deps.clientConfigPath()
	if err != nil {
		return nil, err
	}
	cfg, err := config.LoadClient(cfgPath)
	if err != nil {
		return nil, err
	}
	rows := make([]dashboard.CircuitRow, 0, len(cfg.Circuits))
	for name, c := range cfg.Circuits {
		row := dashboard.CircuitRow{
			Name:    name,
			Host:    c.Host,
			Default: name == cfg.DefaultCircuit,
		}
		if l.deps.statusProbe != nil {
			if probe, err := l.deps.statusProbe(ctx, name); err == nil {
				row.Reachable = true
				row.Lakitu = probe.Version
				row.LatencyMS = probe.LatencyMS
			}
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows, nil
}

// Read-only resource panels: not yet wired against lakitu RPCs.
func (l *liveDataSource) Chest(ctx context.Context) ([]dashboard.ResourceRow, error) {
	return nil, errors.New("chest tab is read-only and not yet wired (see lakitu chest)")
}

func (l *liveDataSource) Characters(ctx context.Context) ([]dashboard.ResourceRow, error) {
	return nil, errors.New("characters tab is read-only and not yet wired (see lakitu character edit)")
}

func (l *liveDataSource) Tunes(ctx context.Context) ([]dashboard.ResourceRow, error) {
	return nil, errors.New("tunes tab is read-only and not yet wired (see lakitu tune edit)")
}

func (l *liveDataSource) Ports(ctx context.Context) ([]dashboard.PortRow, error) {
	// Ports panel is a stub in this PR — plan 13 owns the data layer.
	return []dashboard.PortRow{}, nil
}
