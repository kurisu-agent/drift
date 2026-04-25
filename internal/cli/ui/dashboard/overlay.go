package dashboard

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/kurisu-agent/drift/internal/cli/ui"
)

// Cross-cut overlay primitives — palette, help modal, toast. They
// share visual chrome (RoundedBorder in theme.Border.Focus, bg-tinted
// surface) and a single composition helper, overlayOnto, which stamps
// a small block of text onto a larger frame at given (x, y) cell
// coordinates while preserving ANSI on both sides.

// paletteCommand is one entry in the palette's command list. The
// real registry will be derived from cobra commands; for the rebrand
// loop a static list keeps the scenario reproducible.
type paletteCommand struct {
	keys string // shortcut hint — empty allowed
	name string
	desc string
}

// paletteCommands is the static fuzzy list rendered in the palette
// scenario. Order matches the dashboard nav: tab moves, then
// lifecycle, then top-level commands.
var paletteCommands = []paletteCommand{
	{keys: "1", name: "go to status", desc: "flagship at-a-glance"},
	{keys: "2", name: "go to karts", desc: "kart table across circuits"},
	{keys: "3", name: "go to circuits", desc: "circuit-level admin"},
	{keys: "4", name: "go to chest", desc: "secrets resolvers"},
	{keys: "5", name: "go to characters", desc: "git identities"},
	{keys: "6", name: "go to tunes", desc: "devcontainer fragments"},
	{keys: "7", name: "go to ports", desc: "workstation forwards"},
	{keys: "8", name: "go to logs", desc: "kart log tail"},
	{name: "kart restart", desc: "restart the focused kart"},
	{name: "kart rebuild", desc: "rebuild the focused kart"},
	{name: "kart connect", desc: "open an interactive shell"},
	{name: "drift new", desc: "scaffold a new kart"},
	{name: "drift status", desc: "summary across circuits"},
}

// renderPalette draws the command palette modal at the requested
// inner width. The query string drives a simple substring filter
// against name+desc; the first match is highlighted in the brand
// accent. Used by dashboard.View when palette overlay is open.
func renderPalette(t *ui.Theme, query string, width int) string {
	w := paletteWidth(width)
	header := paletteHeader(t, query, w)

	// Filter and rank.
	q := strings.ToLower(query)
	matches := make([]paletteCommand, 0, len(paletteCommands))
	for _, c := range paletteCommands {
		if q == "" ||
			strings.Contains(strings.ToLower(c.name), q) ||
			strings.Contains(strings.ToLower(c.desc), q) {
			matches = append(matches, c)
		}
	}

	rows := make([]string, 0, len(matches)+2)
	rows = append(rows, header, paletteRule(t, w))
	for i, c := range matches {
		row := palettteRow(t, c, i == 0, w)
		rows = append(rows, row)
	}
	if len(matches) == 0 {
		rows = append(rows, paletteEmpty(t, w))
	}
	body := strings.Join(rows, "\n")
	return paletteFrame(t, body, w)
}

func paletteWidth(width int) int {
	w := width * 60 / 100
	if w > 80 {
		w = 80
	}
	if w < 40 {
		w = 40
	}
	return w
}

func paletteHeader(t *ui.Theme, query string, width int) string {
	prompt := ":" + query
	cursor := "▏"
	body := prompt + cursor
	if t != nil && t.Enabled {
		body = t.AccentStyle.Render(":") + query + t.Border.Focus.Render(cursor)
	}
	return padToCells(body, width-4)
}

func paletteRule(t *ui.Theme, width int) string {
	rule := strings.Repeat("─", width-4)
	if t != nil && t.Enabled {
		return t.Border.Subtle.Render(rule)
	}
	return rule
}

func palettteRow(t *ui.Theme, c paletteCommand, focused bool, width int) string {
	keys := "   "
	if c.keys != "" {
		keys = fmt.Sprintf(" %s ", c.keys)
	}
	name := c.name
	desc := c.desc
	if t != nil && t.Enabled {
		if focused {
			keys = t.Status.Info.Pill.Render(strings.TrimSpace(c.keys + " "))
			name = t.AccentStyle.Bold(true).Render(c.name)
		} else {
			keys = t.MutedStyle.Render(keys)
			name = t.BoldStyle.Render(c.name)
		}
		desc = t.MutedStyle.Render(desc)
	}
	body := keys + " " + name + "  " + desc
	return padToCells(body, width-4)
}

func paletteEmpty(t *ui.Theme, width int) string {
	body := "no matches"
	if t != nil && t.Enabled {
		body = t.MutedStyle.Render(body)
	}
	return padToCells(body, width-4)
}

func paletteFrame(t *ui.Theme, body string, width int) string {
	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Padding(0, 1).
		Width(width - 2)
	if t != nil && t.Enabled {
		style = style.BorderForeground(t.Border.Focus.GetForeground())
	}
	return style.Render(body)
}

// renderHelpModal draws a centred two-column key/description grid
// for the dashboard's full help. Used when the ?-overlay opens.
func renderHelpModal(t *ui.Theme, width int) string {
	w := paletteWidth(width)

	bindings := [][2]string{
		{"tab / shift+tab", "next / previous tab"},
		{"1 – 8", "jump to numbered tab"},
		{"↑ ↓ k j", "move within the focused panel"},
		{"enter", "activate or expand the focused row"},
		{"esc", "back out of a dialog or filter"},
		{"/", "filter the focused panel"},
		{":", "open the command palette"},
		{"?", "open this help modal"},
		{"r", "refresh the focused panel"},
		{"f", "toggle follow mode in logs"},
		{"q / ctrl+c", "quit"},
	}

	rows := make([]string, 0, len(bindings)+2)
	title := "drift help"
	if t != nil && t.Enabled {
		title = t.AccentStyle.Bold(true).Render(title)
	}
	rows = append(rows, padToCells(title, w-4), paletteRule(t, w))

	keyW := 18
	for _, b := range bindings {
		key := padToCells(b[0], keyW)
		desc := b[1]
		if t != nil && t.Enabled {
			key = t.Help.Key.Render(b[0])
			key = padToCells(key, keyW)
			desc = t.Help.Desc.Render(b[1])
		}
		rows = append(rows, padToCells(key+desc, w-4))
	}

	footer := "press ? or esc to close"
	if t != nil && t.Enabled {
		footer = t.MutedStyle.Render(footer)
	}
	rows = append(rows, "", padToCells(footer, w-4))

	body := strings.Join(rows, "\n")
	return paletteFrame(t, body, w)
}

// renderToast draws a single toast box for kind ("success"|"error").
// kind drives the border color and the leading glyph; the message
// renders in default text. Toasts are bottom-right anchored by the
// caller via overlayOnto.
func renderToast(t *ui.Theme, kind, message string) string {
	icon := ui.IconSuccess
	border := lipgloss.RoundedBorder()
	style := lipgloss.NewStyle().Border(border).Padding(0, 1)
	if t != nil && t.Enabled {
		switch kind {
		case "error":
			icon = ui.IconError
			style = style.BorderForeground(t.Status.Error.Text.GetForeground())
		case "warn":
			icon = ui.IconWarning
			style = style.BorderForeground(t.Status.Warn.Text.GetForeground())
		default:
			style = style.BorderForeground(t.Status.Success.Text.GetForeground())
		}
	}
	body := ui.Label(icon, message)
	if t != nil && t.Enabled {
		switch kind {
		case "error":
			body = t.Status.Error.Text.Render(ui.IconError) + " " + message
		case "warn":
			body = t.Status.Warn.Text.Render(ui.IconWarning) + " " + message
		default:
			body = t.Status.Success.Text.Render(ui.IconSuccess) + " " + message
		}
	}
	return style.Render(body)
}

// overlayOnto stamps an overlay block onto a frame at the requested
// cell offset. Both sides may carry ANSI; lines from the overlay
// replace the corresponding cell range in the underlying frame
// without leaking attributes back into the frame's surrounding cells.
func overlayOnto(frame, overlay string, x, y int) string {
	if overlay == "" {
		return frame
	}
	frameLines := strings.Split(frame, "\n")
	overlayLines := strings.Split(overlay, "\n")
	for i, ol := range overlayLines {
		row := y + i
		if row < 0 || row >= len(frameLines) {
			continue
		}
		frameLines[row] = spliceLine(frameLines[row], ol, x)
	}
	return strings.Join(frameLines, "\n")
}

// spliceLine replaces a horizontal slice of `base` with `over` at
// cell offset `x`. ANSI in `base` is preserved on both sides of the
// splice; ANSI in `over` is preserved within the splice. Out-of-
// bounds cuts pad with spaces.
func spliceLine(base, over string, x int) string {
	overW := lipgloss.Width(over)
	leftCells := x
	rightCells := lipgloss.Width(base) - (x + overW)
	left := ansi.Cut(base, 0, leftCells)
	if leftCells > 0 && lipgloss.Width(left) < leftCells {
		left += strings.Repeat(" ", leftCells-lipgloss.Width(left))
	}
	right := ""
	if rightCells > 0 {
		right = ansi.Cut(base, x+overW, lipgloss.Width(base))
	}
	return left + over + ansi.ResetStyle + right
}

// padToCells right-pads s with spaces so its rendered cell width is
// at least n. ANSI is preserved.
func padToCells(s string, n int) string {
	w := lipgloss.Width(s)
	if w >= n {
		return s
	}
	return s + strings.Repeat(" ", n-w)
}
