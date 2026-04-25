package ui

import (
	"fmt"
	"io"
	"text/tabwriter"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"
)

// CellStyle is a closed enum of named cell colors usable in a Table.
// Keeps per-column color choices in one place; subcommand code never
// touches lipgloss directly to colour a cell.
type CellStyle int

const (
	CellDefault CellStyle = iota
	CellAccent
	CellSuccess
	CellWarn
	CellDim
	CellError
)

// Cell is a single rendered cell value plus its desired style.
type Cell struct {
	Text  string
	Style CellStyle
	Bold  bool
}

// CellsFromStrings lifts a row of plain strings into a row of unstyled Cells.
func CellsFromStrings(row []string) []Cell {
	out := make([]Cell, len(row))
	for i, s := range row {
		out[i] = Cell{Text: s}
	}
	return out
}

// Table renders headers + rows. Color path uses lipgloss/v2/table with
// hidden borders; plain path uses tabwriter so CI / piped output stays
// readable. Both paths emit a trailing newline.
func (t *Theme) Table(w io.Writer, headers []string, rows [][]Cell) {
	if t == nil || !t.Enabled {
		writePlainTable(w, headers, cellsToStrings(rows))
		return
	}
	tbl := table.New().
		Border(lipgloss.HiddenBorder()).
		BorderTop(false).BorderBottom(false).
		BorderLeft(false).BorderRight(false).
		BorderColumn(false).BorderRow(false).BorderHeader(false).
		Headers(headers...).
		StyleFunc(func(row, col int) lipgloss.Style {
			if row == table.HeaderRow {
				return t.BoldStyle.PaddingRight(2)
			}
			base := lipgloss.NewStyle().PaddingRight(2)
			if row < 0 || row >= len(rows) || col < 0 || col >= len(rows[row]) {
				return base
			}
			c := rows[row][col]
			st := t.cellLipgloss(c.Style)
			if c.Bold {
				st = st.Bold(true)
			}
			return st.Inherit(base)
		})
	for _, row := range rows {
		strs := make([]string, len(row))
		for i, c := range row {
			strs[i] = c.Text
		}
		tbl.Row(strs...)
	}
	fmt.Fprintln(w, tbl.Render())
}

func (t *Theme) cellLipgloss(s CellStyle) lipgloss.Style {
	switch s {
	case CellAccent:
		return t.AccentStyle
	case CellSuccess:
		return t.SuccessStyle
	case CellWarn:
		return t.WarnStyle
	case CellDim:
		return t.DimStyle
	case CellError:
		return t.ErrorStyle
	}
	return lipgloss.NewStyle()
}

func cellsToStrings(rows [][]Cell) [][]string {
	out := make([][]string, len(rows))
	for i, r := range rows {
		out[i] = make([]string, len(r))
		for j, c := range r {
			out[i][j] = c.Text
		}
	}
	return out
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
