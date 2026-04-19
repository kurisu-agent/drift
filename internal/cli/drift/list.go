package drift

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/charmbracelet/lipgloss"
	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/cli/style"
	"github.com/kurisu-agent/drift/internal/wire"
)

type listCmd struct{}

type infoCmd struct {
	Name string `arg:"" help:"Kart name."`
}

// listEntry renders only these fields; unknown fields pass through via
// raw JSON on --output=json.
type listEntry struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Tune   string `json:"tune,omitempty"`
	Stale  bool   `json:"stale,omitempty"`
	Source struct {
		Mode string `json:"mode"`
		URL  string `json:"url,omitempty"`
	} `json:"source"`
	Autostart bool `json:"autostart"`
}

type listResult struct {
	Karts []listEntry `json:"karts"`
}

func runKartList(ctx context.Context, io IO, root *CLI, _ listCmd, deps deps) int {
	circuit, err := resolveCircuit(root, deps)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	var raw json.RawMessage
	if err := deps.call(ctx, circuit, wire.MethodKartList, struct{}{}, &raw); err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	if root.Output == "json" {
		fmt.Fprintln(io.Stdout, string(raw))
		return 0
	}
	var res listResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	if len(res.Karts) == 0 {
		fmt.Fprintln(io.Stdout, "no karts on this circuit")
		return 0
	}
	p := style.For(io.Stdout, root.Output == "json")
	rows := make([][]string, 0, len(res.Karts))
	staleRows := make([]bool, 0, len(res.Karts))
	for _, k := range res.Karts {
		status := k.Status
		if k.Stale {
			status += " (stale)"
		}
		src := k.Source.Mode
		if k.Source.URL != "" {
			src = k.Source.Mode + " " + k.Source.URL
		}
		autostart := ""
		if k.Autostart {
			autostart = "*"
		}
		tune := k.Tune
		if tune == "" {
			tune = "-"
		}
		rows = append(rows, []string{k.Name, status, src, tune, autostart})
		staleRows = append(staleRows, k.Stale)
	}
	writeTable(io.Stdout, p, []string{"NAME", "STATUS", "SOURCE", "TUNE", "AUTOSTART"}, rows,
		func(row, col int, _ *style.Palette) lipgloss.Style {
			switch col {
			case 0: // NAME
				return lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
			case 1: // STATUS
				if row >= 0 && row < len(staleRows) && staleRows[row] {
					return lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
				}
				switch rows[row][1] {
				case "running":
					return lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
				case "stopped":
					return lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
				}
			}
			return lipgloss.NewStyle()
		})
	return 0
}

func runKartInfo(ctx context.Context, io IO, root *CLI, cmd infoCmd, deps deps) int {
	circuit, err := resolveCircuit(root, deps)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	var raw json.RawMessage
	if err := deps.call(ctx, circuit, wire.MethodKartInfo, map[string]string{"name": cmd.Name}, &raw); err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	// Always pretty-print — info's nested sub-objects don't flatten into
	// a readable table.
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	pretty, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	fmt.Fprintln(io.Stdout, string(pretty))
	return 0
}
