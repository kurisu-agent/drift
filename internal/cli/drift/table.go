package drift

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/kurisu-agent/drift/internal/cli/style"
)

// tableCellStyler receives the zero-indexed row/column of a cell (row == -1
// is the header row) and returns a Palette-aware style applied to the cell
// contents. Called only when the palette is enabled.
type tableCellStyler func(row, col int, p *style.Palette) lipgloss.Style

// writeTable emits a table to w. When the palette is enabled, it uses
// lipgloss/table with no borders — headers bold, per-cell styling via
// cellStyle. When disabled, it falls back to tabwriter so CI logs / piped
// output stay plain.
func writeTable(w io.Writer, p *style.Palette, headers []string, rows [][]string, cellStyle tableCellStyler) {
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
