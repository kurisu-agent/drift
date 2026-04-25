package ui

import (
	"fmt"
	"io"

	"charm.land/lipgloss/v2"
)

// SuccessLine writes a green check + msg.
func (t *Theme) SuccessLine(w io.Writer, msg string) {
	t.statusLine(w, Icon(IconSuccess), msg, t.SuccessStyle)
}

// WarnLine writes a yellow warning + msg.
func (t *Theme) WarnLine(w io.Writer, msg string) {
	t.statusLine(w, Icon(IconWarning), msg, t.WarnStyle)
}

// FailLine writes a red x + msg.
func (t *Theme) FailLine(w io.Writer, msg string) {
	t.statusLine(w, Icon(IconError), msg, t.ErrorStyle)
}

// InfoLine writes a dim info glyph + msg.
func (t *Theme) InfoLine(w io.Writer, msg string) {
	t.statusLine(w, Icon(IconInfo), msg, t.DimStyle)
}

func (t *Theme) statusLine(w io.Writer, glyph, msg string, st lipgloss.Style) {
	if t == nil || !t.Enabled {
		if glyph != "" {
			fmt.Fprintf(w, "%s %s\n", glyph, msg)
		} else {
			fmt.Fprintln(w, msg)
		}
		return
	}
	fmt.Fprintf(w, "%s %s\n", st.Render(glyph), msg)
}
