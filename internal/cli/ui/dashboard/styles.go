package dashboard

import (
	"charm.land/bubbles/v2/table"
	"charm.land/lipgloss/v2"
	"github.com/kurisu-agent/drift/internal/cli/ui"
)

// tableStyles wires bubbles/v2/table to drift's theme. Header bold,
// selected row reverse-styled in accent, rest dim — matches the rest
// of the dashboard chrome.
func tableStyles(t *ui.Theme) table.Styles {
	s := table.DefaultStyles()
	if t == nil || !t.Enabled {
		// Strip styling so NO_COLOR / non-TTY fixtures stay plain.
		s.Header = lipgloss.NewStyle().Bold(true)
		s.Selected = lipgloss.NewStyle()
		s.Cell = lipgloss.NewStyle()
		return s
	}
	s.Header = t.BoldStyle.Padding(0, 1)
	s.Cell = lipgloss.NewStyle().Padding(0, 1)
	s.Selected = t.AccentStyle.Reverse(true).Padding(0, 1)
	return s
}

// panelEmpty centers a small dim message in a width x height area.
// Used for "loading...", "no rows", and other passive states so panels
// have one visual idiom for not-much-to-show.
func panelEmpty(t *ui.Theme, msg string, width, height int) string {
	body := dimFor(t).Render(msg)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, body)
}

// panelError renders a centered error block — same shape as
// panelEmpty but in the theme's error color.
func panelError(t *ui.Theme, msg string, width, height int) string {
	body := errorFor(t).Render("error: ") + msg
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, body)
}

// dimFor / boldFor / errorFor return the relevant lipgloss.Style for
// the theme, falling back to identity when the theme is disabled. They
// keep panels free of `if t.Enabled` branches at every call site.
func dimFor(t *ui.Theme) lipgloss.Style {
	if t == nil || !t.Enabled {
		return lipgloss.NewStyle()
	}
	return t.DimStyle
}

func boldFor(t *ui.Theme) lipgloss.Style {
	if t == nil || !t.Enabled {
		return lipgloss.NewStyle().Bold(true)
	}
	return t.BoldStyle
}

func errorFor(t *ui.Theme) lipgloss.Style {
	if t == nil || !t.Enabled {
		return lipgloss.NewStyle()
	}
	return t.ErrorStyle
}
