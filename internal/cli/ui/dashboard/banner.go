package dashboard

import (
	"image/color"

	"charm.land/lipgloss/v2"
	"github.com/kurisu-agent/drift/internal/cli/ui"
)

// wordmark is the hardcoded "drift" banner from plans/14-tui-redesign.md
// lines 194-196. Three rows, 7 columns of rounded box drawing. The
// plan calls for a Tmplr Rounded approximation; this is the literal
// sketch and must not be redesigned without updating the plan first.
// Hardcoded per the plan's "no runtime figlet renderer" rule.
const wordmark = ` ╮  •╭ ` + "\n" +
	`╭┤╭╮╮┼┼` + "\n" +
	`╰┴╯ ╰╯╰`

// bannerWidth is the column width of the wordmark — used by the
// entrance animation to compute the off-screen start position.
const bannerWidth = 7

// wordmarkGradient returns one color.Color per banner column. nil means
// the theme is disabled (NO_COLOR / non-TTY) and the wordmark should
// render plain. Computed once per call via lipgloss.Blend1D so the
// rainbow is consistent across slices and animation frames.
func wordmarkGradient(t *ui.Theme) []color.Color {
	if t == nil || !t.Enabled {
		return nil
	}
	stops := []color.Color{
		lipgloss.Color("#ff5f5f"),
		lipgloss.Color("#ffaf5f"),
		lipgloss.Color("#ffff5f"),
		lipgloss.Color("#5fff5f"),
		lipgloss.Color("#5fafff"),
		lipgloss.Color("#af5fff"),
	}
	return lipgloss.Blend1D(bannerWidth, stops...)
}
