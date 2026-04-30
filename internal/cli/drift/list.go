package drift

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/cli/style"
	"github.com/kurisu-agent/drift/internal/config"
	"github.com/kurisu-agent/drift/internal/wire"
)

// Plural list-verb commands — all empty structs; the scripting surface is
// table output or `--output json`. Singular noun commands drop into pickers
// (see `drift circuit`, `drift kart`); these are the explicit print paths.
type (
	circuitsCmd struct{}
	kartsCmd    struct{}
	runsCmd     struct{}
	skillsCmd   struct{}
	patsCmd     struct{}
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
	Autostart bool          `json:"autostart"`
	PAT       *listEntryPAT `json:"pat,omitempty"`
}

// listEntryPAT mirrors the server's KartInfoPAT shape, byte-identical
// JSON tags. Kept local so list.go doesn't pull in the full server
// package; Name / Owner are populated by kart.info but ignored by the
// list table renderer.
type listEntryPAT struct {
	Slug          string `json:"slug"`
	Name          string `json:"name,omitempty"`
	Owner         string `json:"owner,omitempty"`
	ExpiresAt     string `json:"expires_at,omitempty"`
	DaysRemaining *int   `json:"days_remaining,omitempty"`
	Missing       bool   `json:"missing,omitempty"`
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
//
// PAT column is only rendered when at least one entry has a `pat_slug`
// recorded — keeps the kart list narrow on circuits that don't use the
// registry. When present, the cell shows the slug plus a soft-warning
// or hard-error glyph if the underlying PAT is within 14 days of expiry
// or already past it / missing from the registry.
func writeKartListTable(w io.Writer, p *style.Palette, entries []listEntry) {
	rows := make([][]string, 0, len(entries))
	staleRows := make([]bool, 0, len(entries))
	patWarnRows := make([]bool, 0, len(entries))
	anyPAT := false
	for _, k := range entries {
		if k.PAT != nil {
			anyPAT = true
		}
	}
	headers := []string{"NAME", "STATUS", "SOURCE", "TUNE", "AUTOSTART"}
	if anyPAT {
		headers = append(headers, "PAT")
	}
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
		row := []string{k.Name, status, src, tune, autostart}
		patWarn := false
		if anyPAT {
			cell, warn := formatPATListCell(k.PAT)
			row = append(row, cell)
			patWarn = warn
		}
		rows = append(rows, row)
		staleRows = append(staleRows, k.Stale)
		patWarnRows = append(patWarnRows, patWarn)
	}
	patCol := -1
	if anyPAT {
		patCol = len(headers) - 1
	}
	writeTable(w, p, headers, rows,
		colorCellStyler(func(row, col int) tableCell {
			switch {
			case col == 0:
				return tableCell{Color: tableCellAccent}
			case col == 1:
				if row >= 0 && row < len(staleRows) && staleRows[row] {
					return tableCell{Color: tableCellWarn}
				}
				switch rows[row][1] {
				case "running":
					return tableCell{Color: tableCellSuccess}
				case "stopped":
					return tableCell{Color: tableCellDim}
				}
			case col == patCol && row >= 0 && row < len(patWarnRows) && patWarnRows[row]:
				return tableCell{Color: tableCellWarn}
			}
			return tableCell{}
		}))
}

// formatPATInfoRow renders the PAT row in `drift kart info`. Layout:
//
//	<slug>  @<owner>  expires <date> (Nd[!]) [name]
//
// Annotates the days-remaining segment with `⚠` at ≤14d and `✗` at <0
// so the operator's eye snaps to the rotation work even on monochrome.
// A missing PAT (slug points at a deleted registry record) renders as
// `<slug> ✗ missing` with no further fields.
func formatPATInfoRow(p *listEntryPAT, pal *style.Palette) string {
	if p == nil {
		return ""
	}
	if p.Missing {
		return fmt.Sprintf("%s %s", p.Slug, pal.Warn("✗ missing"))
	}
	parts := []string{p.Slug}
	if p.Owner != "" {
		parts = append(parts, "@"+p.Owner)
	}
	if p.ExpiresAt != "" {
		expiry := "expires " + p.ExpiresAt
		if p.DaysRemaining != nil {
			d := *p.DaysRemaining
			switch {
			case d < 0:
				expiry = pal.Warn(fmt.Sprintf("expires %s ✗ expired", p.ExpiresAt))
			case d <= 14:
				expiry = pal.Warn(fmt.Sprintf("expires %s ⚠ %dd", p.ExpiresAt, d))
			default:
				expiry = fmt.Sprintf("expires %s (%dd)", p.ExpiresAt, d)
			}
		}
		parts = append(parts, expiry)
	}
	if p.Name != "" && p.Name != p.Slug {
		parts = append(parts, pal.Dim("· "+p.Name))
	}
	return strings.Join(parts, "  ")
}

// formatPATListCell renders the PAT column for one kart row. Returns the
// cell text and a "should this row be warn-tinted" bool. A nil entry
// becomes empty so karts without a slug stay quiet.
//
// Threshold: 14 days. At ≤14 the cell shows `<slug> ⚠ Nd`, at ≤0 it
// shows `<slug> ✗ expired`, and a missing slug shows `<slug> ✗ missing`.
// All three trigger the warn tint; the unicode prefix lets monochrome
// terminals still see the signal.
func formatPATListCell(p *listEntryPAT) (string, bool) {
	if p == nil {
		return "", false
	}
	if p.Missing {
		return p.Slug + " ✗ missing", true
	}
	if p.DaysRemaining == nil {
		return p.Slug, false
	}
	d := *p.DaysRemaining
	switch {
	case d < 0:
		return fmt.Sprintf("%s ✗ expired", p.Slug), true
	case d <= 14:
		return fmt.Sprintf("%s ⚠ %dd", p.Slug, d), true
	}
	return p.Slug, false
}

// runCircuits is `drift circuits`. Thin wrapper over runCircuitList so the
// printing path stays in circuit.go and this file owns the list verbs.
func runCircuits(io IO, root *CLI, deps deps) int {
	return runCircuitList(io, root, deps)
}

// runKarts is `drift karts`. Cross-circuit by default; `-c <name>` scopes
// to a single circuit and omits the CIRCUIT column for the lighter look
// users already know from `drift status`.
func runKarts(ctx context.Context, io IO, root *CLI, deps deps) int {
	if root.Circuit != "" {
		return runKartListForCircuit(ctx, io, root, deps)
	}
	return runKartsCrossCircuit(ctx, io, root, deps)
}

// runRuns is `drift runs`. Wrapper kept alongside the other plural verbs
// for symmetry; the heavy lifting lives in run.go.
func runRuns(ctx context.Context, io IO, root *CLI, deps deps) int {
	return runRunsList(ctx, io, root, deps)
}

// runSkills is `drift skills`. Wrapper over renderSkillsOutput — the plural
// path never drops into the interactive tail, so it's safe to call from
// scripts and --output json.
func runSkills(ctx context.Context, io IO, root *CLI, deps deps) int {
	_, circuit, err := resolveCircuit(root, deps)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	var list wire.SkillListResult
	if err := deps.call(ctx, circuit, wire.MethodSkillList, struct{}{}, &list); err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	return renderSkillsOutput(io, root, list)
}

// runKartsCrossCircuit fans kart.list out to every configured circuit in
// parallel and renders one combined table sorted by last-used descending.
// Mirrors the merged picker on `drift connect` so the data shape and
// ordering stay consistent.
func runKartsCrossCircuit(ctx context.Context, io IO, root *CLI, deps deps) int {
	cfgPath, err := deps.clientConfigPath()
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	cfg, err := config.LoadClient(cfgPath)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	if len(cfg.Circuits) == 0 {
		if root.Output == "json" {
			return emitJSON(io, struct {
				Karts []circuitKartJSON `json:"karts"`
			}{Karts: []circuitKartJSON{}})
		}
		fmt.Fprintln(io.Stdout, "no circuits configured (try `drift circuit add user@host`)")
		return 0
	}

	karts, probeErrs := collectCircuitKarts(ctx, cfg, deps)
	// Surface per-circuit probe failures on stderr so the listing still
	// renders for the reachable circuits; users see exactly what broke.
	for circuit, perr := range probeErrs {
		fmt.Fprintf(io.Stderr, "warning: %s: %v\n", circuit, perr)
	}
	sortByLastUsedDesc(karts)

	if root.Output == "json" {
		entries := make([]circuitKartJSON, 0, len(karts))
		for _, k := range karts {
			entries = append(entries, circuitKartJSON(k))
		}
		return emitJSON(io, struct {
			Karts []circuitKartJSON `json:"karts"`
		}{Karts: entries})
	}
	if len(karts) == 0 {
		fmt.Fprintln(io.Stdout, "no karts found on any configured circuit")
		return 0
	}
	writeCrossCircuitKartTable(io.Stdout, style.For(io.Stdout, false), karts)
	return 0
}

// circuitKartJSON is the per-row JSON shape for `drift karts --output json`.
// Flat circuit+entry pair mirrors the table layout so jq one-liners can
// group/filter on `.circuit` without digging into a nested object.
type circuitKartJSON struct {
	Circuit string    `json:"circuit"`
	Entry   listEntry `json:"entry"`
}

// writeCrossCircuitKartTable renders the cross-circuit roster with a
// CIRCUIT column prepended. Shares status colour rules with
// writeKartListTable so the two views look consistent in `drift status`
// right next to `drift karts`.
func writeCrossCircuitKartTable(w io.Writer, p *style.Palette, karts []circuitKart) {
	rows := make([][]string, 0, len(karts))
	staleRows := make([]bool, 0, len(karts))
	for _, ck := range karts {
		k := ck.Entry
		status := k.Status
		if k.Stale {
			status += " (stale)"
		}
		src := k.Source.Mode
		if k.Source.URL != "" {
			src = k.Source.Mode + " " + k.Source.URL
		}
		rows = append(rows, []string{ck.Circuit, k.Name, status, src})
		staleRows = append(staleRows, k.Stale)
	}
	writeTable(w, p, []string{"CIRCUIT", "NAME", "STATUS", "SOURCE"}, rows,
		colorCellStyler(func(row, col int) tableCell {
			switch col {
			case 0:
				return tableCell{Color: tableCellDim}
			case 1:
				return tableCell{Color: tableCellAccent}
			case 2:
				if row >= 0 && row < len(staleRows) && staleRows[row] {
					return tableCell{Color: tableCellWarn}
				}
				switch rows[row][2] {
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
// Entry point for `drift karts -c <name>` and the per-circuit blocks in
// `drift status`. JSON mode streams the raw kart.list payload through
// verbatim so additive server fields survive.
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
		Tune             string `json:"tune,omitempty"`
		Character        string `json:"character,omitempty"`
		CharacterDisplay *struct {
			DisplayName string `json:"display_name,omitempty"`
			Icon        string `json:"icon,omitempty"`
			Glyph       string `json:"glyph,omitempty"`
			Color       string `json:"color,omitempty"`
		} `json:"character_display,omitempty"`
		Autostart bool `json:"autostart"`
		Stale     bool `json:"stale,omitempty"`
		Container *struct {
			Image string `json:"image,omitempty"`
		} `json:"container,omitempty"`
		Devpod *struct {
			WorkspaceID string `json:"workspace_id,omitempty"`
			Provider    string `json:"provider,omitempty"`
		} `json:"devpod,omitempty"`
		PAT *listEntryPAT `json:"pat,omitempty"`
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
	character := info.Character
	if d := info.CharacterDisplay; d != nil {
		// Render: <yaml-key> · <glyph> <display_name>
		// Only attach the suffix when the operator wrote a display_name
		// or icon — otherwise the YAML key already carries everything.
		var extras []string
		if d.Glyph != "" {
			extras = append(extras, d.Glyph)
		}
		if d.DisplayName != "" && d.DisplayName != info.Character {
			extras = append(extras, d.DisplayName)
		}
		if len(extras) > 0 {
			character = fmt.Sprintf("%s %s", info.Character, p.Dim("· "+strings.Join(extras, " ")))
		}
	}
	printIf("source", src)
	printIf("tune", info.Tune)
	printIf("character", character)
	if info.PAT != nil {
		printIf("pat", formatPATInfoRow(info.PAT, p))
	}
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
