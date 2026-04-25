package ui

import (
	"fmt"
	"io"
	"strings"
)

// Header writes a page-title block. Renders as one bold line followed by
// a dim subtitle when sub is non-empty.
func (t *Theme) Header(w io.Writer, title, sub string) {
	if t == nil || !t.Enabled {
		fmt.Fprintln(w, title)
		if sub != "" {
			fmt.Fprintln(w, sub)
		}
		return
	}
	fmt.Fprintln(w, t.BoldStyle.Render(title))
	if sub != "" {
		fmt.Fprintln(w, t.DimStyle.Render(sub))
	}
}

// SectionHeader writes a smaller dim section title with a trailing rule.
func (t *Theme) SectionHeader(w io.Writer, title string) {
	rule := strings.Repeat("─", maxLen(title, 20))
	if t == nil || !t.Enabled {
		fmt.Fprintln(w, title)
		fmt.Fprintln(w, rule)
		return
	}
	fmt.Fprintln(w, t.AccentStyle.Render(title))
	fmt.Fprintln(w, t.DimStyle.Render(rule))
}

func maxLen(s string, min int) int {
	if len(s) > min {
		return len(s)
	}
	return min
}
