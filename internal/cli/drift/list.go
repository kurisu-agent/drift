package drift

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/cli/style"
	"github.com/kurisu-agent/drift/internal/wire"
)

type infoCmd struct {
	Name string `arg:"" help:"Kart name."`
}

// listEntry renders only these fields; unknown fields pass through via
// raw JSON on --output=json.
type listEntry struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	Tune      string `json:"tune,omitempty"`
	Stale     bool   `json:"stale,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	LastUsed  string `json:"last_used,omitempty"`
	Source    struct {
		Mode string `json:"mode"`
		URL  string `json:"url,omitempty"`
	} `json:"source"`
	Autostart bool `json:"autostart"`
}

type listResult struct {
	Karts []listEntry `json:"karts"`
}

// fetchKartList makes one kart.list RPC and returns both the typed
// listEntry slice and the raw JSON. Callers that need to emit JSON
// verbatim (preserving any fields this client doesn't know) keep the
// raw form; text renderers use the typed slice.
func fetchKartList(ctx context.Context, deps deps, circuit string) ([]listEntry, json.RawMessage, error) {
	var raw json.RawMessage
	if err := deps.call(ctx, circuit, wire.MethodKartList, struct{}{}, &raw); err != nil {
		return nil, nil, err
	}
	var res listResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, raw, err
	}
	return res.Karts, raw, nil
}

// writeKartListTable renders a text-mode kart table. Shared between
// `drift connect -l` and the per-circuit kart blocks in `drift status`.
func writeKartListTable(w io.Writer, p *style.Palette, entries []listEntry) {
	rows := make([][]string, 0, len(entries))
	staleRows := make([]bool, 0, len(entries))
	for _, k := range entries {
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
	writeTable(w, p, []string{"NAME", "STATUS", "SOURCE", "TUNE", "AUTOSTART"}, rows,
		colorCellStyler(func(row, col int) tableCell {
			switch col {
			case 0:
				return tableCell{Color: tableCellAccent}
			case 1:
				if row >= 0 && row < len(staleRows) && staleRows[row] {
					return tableCell{Color: tableCellWarn}
				}
				switch rows[row][1] {
				case "running":
					return tableCell{Color: tableCellSuccess}
				case "stopped":
					return tableCell{Color: tableCellDim}
				}
			}
			return tableCell{}
		}))
}

// runKartListForCircuit renders the kart list for the target circuit.
// Entry point for `drift connect -l` / `drift connect --list` and any
// future list-style caller. JSON mode streams the raw kart.list payload
// through verbatim so additive server fields survive.
func runKartListForCircuit(ctx context.Context, io IO, root *CLI, deps deps) int {
	_, circuit, err := resolveCircuit(root, deps)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	entries, raw, err := fetchKartList(ctx, deps, circuit)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	if root.Output == "json" {
		fmt.Fprintln(io.Stdout, string(raw))
		return 0
	}
	if len(entries) == 0 {
		fmt.Fprintln(io.Stdout, "no karts on this circuit")
		return 0
	}
	writeKartListTable(io.Stdout, style.For(io.Stdout, false), entries)
	return 0
}

func runKartInfo(ctx context.Context, io IO, root *CLI, cmd infoCmd, deps deps) int {
	_, circuit, err := resolveCircuit(root, deps)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	var raw json.RawMessage
	if err := deps.call(ctx, circuit, wire.MethodKartInfo, map[string]string{"name": cmd.Name}, &raw); err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	if root != nil && root.Output == "json" {
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
	return renderInfoText(io, raw)
}

// renderInfoText prints a key/value block keyed off the stable fields in
// kart.info. Anything the server adds that we don't know how to lay out
// is rendered compact at the bottom via json.MarshalIndent so users still
// see it on `--output text`.
func renderInfoText(io IO, raw json.RawMessage) int {
	p := style.For(io.Stdout, false)
	var info struct {
		Name      string `json:"name"`
		Status    string `json:"status"`
		CreatedAt string `json:"created_at,omitempty"`
		LastUsed  string `json:"last_used,omitempty"`
		Source    struct {
			Mode string `json:"mode"`
			URL  string `json:"url,omitempty"`
		} `json:"source"`
		Tune      string `json:"tune,omitempty"`
		Character string `json:"character,omitempty"`
		Autostart bool   `json:"autostart"`
		Stale     bool   `json:"stale,omitempty"`
		Container *struct {
			Image string `json:"image,omitempty"`
		} `json:"container,omitempty"`
		Devpod *struct {
			WorkspaceID string `json:"workspace_id,omitempty"`
			Provider    string `json:"provider,omitempty"`
		} `json:"devpod,omitempty"`
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		return errfmt.Emit(io.Stderr, err)
	}

	status := info.Status
	if info.Stale {
		status += " (stale)"
	}
	fmt.Fprintf(io.Stdout, "%s %s\n", p.Bold(p.Accent(info.Name)), p.Dim("("+status+")"))

	printIf := func(label, value string) {
		if value == "" {
			return
		}
		fmt.Fprintf(io.Stdout, "  %s %s\n", p.Dim(label+":"), value)
	}
	src := info.Source.Mode
	if info.Source.URL != "" {
		src = info.Source.Mode + " " + info.Source.URL
	}
	printIf("source", src)
	printIf("tune", info.Tune)
	printIf("character", info.Character)
	printIf("created", info.CreatedAt)
	printIf("last used", info.LastUsed)
	if info.Autostart {
		printIf("autostart", "enabled")
	}
	if info.Container != nil {
		printIf("image", info.Container.Image)
	}
	if info.Devpod != nil {
		if info.Devpod.WorkspaceID != "" {
			printIf("workspace", info.Devpod.WorkspaceID)
		}
		if info.Devpod.Provider != "" {
			printIf("provider", info.Devpod.Provider)
		}
	}
	return 0
}
