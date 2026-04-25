package dashboard

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/kurisu-agent/drift/internal/cli/ui"
)

// wordmark is the hardcoded "drift" banner. Four rows, 23 columns,
// figlet "small" letterforms — readable as "drift" at a glance, and
// every line is padded to the same width so the rainbow gradient
// blends evenly across columns. The plan suggested Tmplr Rounded; we
// chose readability over font fidelity. No runtime figlet renderer
// per the plan's no-deps rule.
const wordmark = "     _      _   __ _   \n" +
	"  __| |_ _ (_) / _| |_ \n" +
	" / _' | '_|| ||  _|  _|\n" +
	" \\__,_|_|  |_||_|  \\__|"

// bannerWidth is the column width of the wordmark — used by the
// entrance animation to compute the off-screen start position.
const bannerWidth = 23

// renderWordmark applies a horizontal rainbow gradient to the wordmark
// glyphs. The gradient is computed once per render pass via
// lipgloss.Blend1D over the wordmark's column width. When the theme is
// disabled (NO_COLOR / non-TTY) the wordmark renders plain so terminals
// without color don't print SGR escapes.
func renderWordmark(t *ui.Theme) string {
	if t == nil || !t.Enabled {
		return wordmark
	}
	stops := []color.Color{
		lipgloss.Color("#ff5f5f"),
		lipgloss.Color("#ffaf5f"),
		lipgloss.Color("#ffff5f"),
		lipgloss.Color("#5fff5f"),
		lipgloss.Color("#5fafff"),
		lipgloss.Color("#af5fff"),
	}
	colors := lipgloss.Blend1D(bannerWidth, stops...)
	lines := strings.Split(wordmark, "\n")
	out := make([]string, len(lines))
	for i, line := range lines {
		var b strings.Builder
		runes := []rune(line)
		for c, r := range runes {
			if c >= len(colors) {
				b.WriteRune(r)
				continue
			}
			b.WriteString(lipgloss.NewStyle().Foreground(colors[c]).Render(string(r)))
		}
		out[i] = b.String()
	}
	return strings.Join(out, "\n")
}
