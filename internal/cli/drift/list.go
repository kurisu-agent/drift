package drift

import (
	"context"
	"encoding/json"
	"fmt"
	"text/tabwriter"

	"github.com/kurisu-agent/drift/internal/wire"
)

// listCmd is `drift list` — shows karts on the target circuit.
type listCmd struct{}

// infoCmd is `drift info <kart>` — single-kart JSON view. Useful for
// scripting and for debugging stale state.
type infoCmd struct {
	Name string `arg:"" help:"Kart name."`
}

// listEntry mirrors the fields of server.KartInfo the client renders.
// Unknown fields (forward compat) pass through on --output=json via
// raw JSON passthrough; the table rendering only needs these few.
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
		return emitError(io, err)
	}
	var raw json.RawMessage
	if err := deps.call(ctx, circuit, wire.MethodKartList, struct{}{}, &raw); err != nil {
		return emitError(io, err)
	}
	if root.Output == "json" {
		fmt.Fprintln(io.Stdout, string(raw))
		return 0
	}
	var res listResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return emitError(io, err)
	}
	if len(res.Karts) == 0 {
		fmt.Fprintln(io.Stdout, "no karts on this circuit")
		return 0
	}
	tw := tabwriter.NewWriter(io.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSTATUS\tSOURCE\tTUNE\tAUTOSTART")
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
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", k.Name, status, src, tune, autostart)
	}
	_ = tw.Flush()
	return 0
}

func runKartInfo(ctx context.Context, io IO, root *CLI, cmd infoCmd, deps deps) int {
	circuit, err := resolveCircuit(root, deps)
	if err != nil {
		return emitError(io, err)
	}
	var raw json.RawMessage
	if err := deps.call(ctx, circuit, wire.MethodKartInfo, map[string]string{"name": cmd.Name}, &raw); err != nil {
		return emitError(io, err)
	}
	// Always pretty-print JSON — info is inherently structured and the
	// container / devpod sub-objects don't flatten into a readable table.
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return emitError(io, err)
	}
	pretty, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return emitError(io, err)
	}
	fmt.Fprintln(io.Stdout, string(pretty))
	return 0
}
