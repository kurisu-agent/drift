package drift

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/cli/style"
	"github.com/kurisu-agent/drift/internal/config"
	"github.com/kurisu-agent/drift/internal/version"
	"github.com/kurisu-agent/drift/internal/wire"
	"golang.org/x/sync/errgroup"
)

type statusCmd struct {
	NoProbe bool `name:"no-probe" help:"Skip the server.version + kart.list round-trips (show client state only)."`
}

// statusCircuit is the per-circuit payload in both text and JSON modes.
// Probe and kart-count fields are zero/empty when --no-probe or the probe
// failed; ProbeError carries the error string so JSON consumers can branch.
type statusCircuit struct {
	Name       string `json:"name"`
	Host       string `json:"host"`
	Default    bool   `json:"default"`
	Lakitu     string `json:"lakitu_version,omitempty"`
	API        int    `json:"api,omitempty"`
	LatencyMS  int64  `json:"latency_ms,omitempty"`
	Karts      int    `json:"karts,omitempty"`
	Running    int    `json:"running,omitempty"`
	ProbeError string `json:"probe_error,omitempty"`
}

type statusResult struct {
	Drift          string          `json:"drift_version"`
	DefaultCircuit string          `json:"default_circuit,omitempty"`
	Circuits       []statusCircuit `json:"circuits"`
}

func runStatus(ctx context.Context, io IO, root *CLI, cmd statusCmd, deps deps) int {
	cfgPath, err := deps.clientConfigPath()
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	cfg, err := config.LoadClient(cfgPath)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}

	names := make([]string, 0, len(cfg.Circuits))
	for n := range cfg.Circuits {
		names = append(names, n)
	}
	sort.Strings(names)

	// Pre-size the slice so probes write into their owning index; order
	// stays deterministic regardless of which probe finishes first.
	circuits := make([]statusCircuit, len(names))
	for i, n := range names {
		circuits[i] = statusCircuit{
			Name:    n,
			Host:    cfg.Circuits[n].Host,
			Default: n == cfg.DefaultCircuit,
		}
	}
	if !cmd.NoProbe {
		var eg errgroup.Group
		eg.SetLimit(4)
		for i := range circuits {
			eg.Go(func() error {
				fillProbe(ctx, deps, circuits[i].Name, &circuits[i])
				return nil
			})
		}
		// fillProbe never returns non-nil — probe errors land in
		// sc.ProbeError — so we drop the (always-nil) Wait() result.
		_ = eg.Wait()
	}
	res := statusResult{
		Drift:          version.Get().Version,
		DefaultCircuit: cfg.DefaultCircuit,
		Circuits:       circuits,
	}

	if root != nil && root.Output == "json" {
		return emitJSON(io, res)
	}

	p := style.For(io.Stdout, false)
	fmt.Fprintf(io.Stdout, "%s %s\n", p.Bold("drift"), res.Drift)
	if len(res.Circuits) == 0 {
		fmt.Fprintln(io.Stdout, p.Dim("no circuits configured (try `drift init` or `drift circuit add`)"))
		return 0
	}
	// Promote the default to a prominent line under the banner so it's
	// not buried in a column. The picker hint nudges users with >1
	// circuit toward the set-default command.
	if res.DefaultCircuit != "" {
		fmt.Fprintf(io.Stdout, "%s %s\n",
			p.Dim("default:"), p.Accent(res.DefaultCircuit))
	} else {
		fmt.Fprintln(io.Stdout, p.Warn("no default circuit set (run `drift circuit set default`)"))
	}
	if len(res.Circuits) > 1 {
		fmt.Fprintln(io.Stdout, p.Dim("  (change with `drift circuit set default [name]`)"))
	}
	fmt.Fprintln(io.Stdout)

	// Prefix the default row with a chevron so the eye finds it even
	// when the table has scrolled off the top of the terminal. The
	// DEFAULT column is gone — one visual indicator is enough.
	headers := []string{"", "CIRCUIT", "HOST", "LAKITU", "API", "LATENCY", "KARTS"}
	rows := make([][]string, 0, len(res.Circuits))
	probeFailed := make([]bool, 0, len(res.Circuits))
	defaultRow := -1
	for i, sc := range res.Circuits {
		lakitu := sc.Lakitu
		api := ""
		latency := ""
		karts := ""
		switch {
		case sc.ProbeError != "":
			lakitu = "?"
			api = "?"
			latency = "unreachable"
		case cmd.NoProbe:
			lakitu = "-"
			api = "-"
			latency = "-"
			karts = "-"
		default:
			api = fmt.Sprintf("%d", sc.API)
			latency = fmt.Sprintf("%dms", sc.LatencyMS)
			karts = fmt.Sprintf("%d/%d", sc.Running, sc.Karts)
		}
		marker := " "
		if sc.Default {
			marker = "→"
			defaultRow = i
		}
		rows = append(rows, []string{marker, sc.Name, sc.Host, lakitu, api, latency, karts})
		probeFailed = append(probeFailed, sc.ProbeError != "")
	}
	writeTable(io.Stdout, p, headers, rows, colorCellStyler(func(row, col int) tableCell {
		switch col {
		case 0: // chevron marker
			if row == defaultRow {
				return tableCell{Color: tableCellSuccess, Bold: true}
			}
		case 1: // CIRCUIT name
			return tableCell{Color: tableCellAccent, Bold: row == defaultRow}
		case 3, 4, 5, 6: // LAKITU / API / LATENCY / KARTS
			if row >= 0 && row < len(probeFailed) && probeFailed[row] {
				return tableCell{Color: tableCellError}
			}
		}
		return tableCell{}
	}))

	// Emit any probe errors as dim hints below the table so they don't
	// crowd the row layout but still give the user a "why."
	for _, sc := range res.Circuits {
		if sc.ProbeError == "" {
			continue
		}
		fmt.Fprintf(io.Stdout, "%s\n", p.Dim(fmt.Sprintf("  %s: %s", sc.Name, sc.ProbeError)))
	}
	return 0
}

// fillProbe populates sc.Lakitu / API / LatencyMS, plus Karts / Running
// when the kart.list round-trip succeeds. Any failure lands in
// ProbeError — status is a read-only overview, never aborts.
func fillProbe(ctx context.Context, deps deps, circuit string, sc *statusCircuit) {
	if deps.probe == nil {
		sc.ProbeError = "probe not configured"
		return
	}
	pr, err := deps.probe(ctx, circuit)
	if err != nil {
		sc.ProbeError = err.Error()
		return
	}
	sc.Lakitu = pr.Version
	sc.API = pr.API
	sc.LatencyMS = pr.LatencyMS

	var raw json.RawMessage
	if err := deps.call(ctx, circuit, wire.MethodKartList, struct{}{}, &raw); err != nil {
		sc.ProbeError = fmt.Sprintf("kart.list: %v", err)
		return
	}
	var lr struct {
		Karts []struct {
			Status string `json:"status"`
		} `json:"karts"`
	}
	if err := json.Unmarshal(raw, &lr); err != nil {
		sc.ProbeError = fmt.Sprintf("parse kart.list: %v", err)
		return
	}
	sc.Karts = len(lr.Karts)
	for _, k := range lr.Karts {
		if k.Status == "running" {
			sc.Running++
		}
	}
}
