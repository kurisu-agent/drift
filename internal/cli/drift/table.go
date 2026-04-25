package drift

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"
	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/cli/ui"
)

// emitJSON marshals v as a single-line JSON object and writes it to
// io.Stdout with a trailing newline. Marshal failures land on io.Stderr
// as an errfmt-formatted "error:" line and the helper returns the
// errfmt exit code — matches the 7+ hand-rolled copies it replaces.
func emitJSON(io IO, v any) int {
	buf, err := json.Marshal(v)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	fmt.Fprintln(io.Stdout, string(buf))
	return 0
}

// accentCellStyler returns a tableCellStyler that paints the given
// zero-indexed column with the palette's accent color. Other columns
// get the default (unstyled) cell. Matches the 4 call sites that used
// an inline closure with lipgloss.Color("6").
func accentCellStyler(col int) tableCellStyler {
	accent := lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	return func(_, c int, _ *ui.Theme) lipgloss.Style {
		if c == col {
			return accent
		}
		return lipgloss.NewStyle()
	}
}

// tableCellColor is the closed set of named cell colors the drift tables
// use. Keeping the enum + styler here lets callers stay off a direct
// lipgloss import — the palette's color choices only live in one place.
type tableCellColor int

const (
	tableCellDefault tableCellColor = iota
	tableCellAccent                 // 6 — accent / circuit / kart name
	tableCellSuccess                // 2 — "running"
	tableCellWarn                   // 3 — "stale"
	tableCellDim                    // 8 — "stopped"
	tableCellError                  // 1 — "unreachable"
)

// tableCell describes how writeTable should style one cell — a named
// color plus an optional bold flag. Zero value is the unstyled default.
type tableCell struct {
	Color tableCellColor
	Bold  bool
}

// colorCellStyler wraps a per-cell color resolver into a tableCellStyler.
// Callers implement the small closure without touching lipgloss.
func colorCellStyler(fn func(row, col int) tableCell) tableCellStyler {
	return func(row, col int, _ *ui.Theme) lipgloss.Style {
		return styleForCell(fn(row, col))
	}
}

// styleForCell maps a tableCell → lipgloss.Style.
func styleForCell(c tableCell) lipgloss.Style {
	var s lipgloss.Style
	switch c.Color {
	case tableCellAccent:
		s = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	case tableCellSuccess:
		s = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	case tableCellWarn:
		s = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	case tableCellDim:
		s = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	case tableCellError:
		s = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	default:
		s = lipgloss.NewStyle()
	}
	if c.Bold {
		s = s.Bold(true)
	}
	return s
}

// tableCellStyler receives the zero-indexed row/column of a cell (row == -1
// is the header row) and returns a Palette-aware style applied to the cell
// contents. Called only when the palette is enabled.
type tableCellStyler func(row, col int, p *ui.Theme) lipgloss.Style

// writeTable emits a table to w. When the palette is enabled, it uses
// lipgloss/table with no borders — headers bold, per-cell styling via
// cellStyle. When disabled, it falls back to tabwriter so CI logs / piped
// output stay plain.
func writeTable(w io.Writer, p *ui.Theme, headers []string, rows [][]string, cellStyle tableCellStyler) {
	if p == nil || !p.Enabled {
		writePlainTable(w, headers, rows)
		return
	}

	headerStyle := lipgloss.NewStyle().Bold(true)
	t := table.New().
		Border(lipgloss.HiddenBorder()).
		BorderTop(false).BorderBottom(false).
		BorderLeft(false).BorderRight(false).
		BorderColumn(false).BorderRow(false).BorderHeader(false).
		Headers(headers...).
		Rows(rows...).
		StyleFunc(func(row, col int) lipgloss.Style {
			if row == table.HeaderRow {
				return headerStyle.PaddingRight(2)
			}
			base := lipgloss.NewStyle().PaddingRight(2)
			if cellStyle == nil {
				return base
			}
			return cellStyle(row, col, p).Inherit(base)
		})

	fmt.Fprintln(w, t.Render())
}

func writePlainTable(w io.Writer, headers []string, rows [][]string) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for i, h := range headers {
		if i > 0 {
			fmt.Fprint(tw, "\t")
		}
		fmt.Fprint(tw, h)
	}
	fmt.Fprintln(tw)
	for _, r := range rows {
		for i, c := range r {
			if i > 0 {
				fmt.Fprint(tw, "\t")
			}
			fmt.Fprint(tw, c)
		}
		fmt.Fprintln(tw)
	}
	_ = tw.Flush()
}
