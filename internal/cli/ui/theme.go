package ui

import (
	"io"
	"os"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
)

// Theme carries the resolved palette plus a render-mode flag. Methods on
// Theme accept a raw string and return either a styled string (when
// Enabled is true) or the input unchanged (no-op palette). This shape
// matches how the legacy ui.Theme was used so call sites stay
// unconditional: fmt.Fprintln(w, t.Accent(name)).
type Theme struct {
	// Enabled toggles rendering. False under JSON / NO_COLOR / non-TTY.
	Enabled bool
	// Dark is true when the terminal background is dark.
	Dark bool
	// Profile is the detected color profile (NoTTY/ANSI/ANSI256/TrueColor).
	Profile colorprofile.Profile

	AccentStyle  lipgloss.Style
	SuccessStyle lipgloss.Style
	WarnStyle    lipgloss.Style
	ErrorStyle   lipgloss.Style
	DimStyle     lipgloss.Style
	MutedStyle   lipgloss.Style
	BoldStyle    lipgloss.Style

	BorderFocused lipgloss.Style
	BorderBlurred lipgloss.Style

	KeyBindingStyle     lipgloss.Style
	KeyDescriptionStyle lipgloss.Style
}

// NewTheme builds a Theme appropriate for w. jsonMode short-circuits to a
// no-op theme so --output json never gains ANSI. NO_COLOR also yields a
// no-op theme. The DRIFT_THEME env var overrides automatic light/dark
// detection (set to "light" or "dark").
func NewTheme(w io.Writer, jsonMode bool) *Theme {
	enabled := !jsonMode && os.Getenv("NO_COLOR") == "" && isTTY(w)
	dark := resolveDark(w)

	profile := colorprofile.NoTTY
	if enabled {
		profile = colorprofile.Detect(w, os.Environ())
	}

	t := &Theme{
		Enabled: enabled,
		Dark:    dark,
		Profile: profile,
	}
	if enabled {
		buildPalette(t)
	}
	return t
}

func resolveDark(w io.Writer) bool {
	switch os.Getenv("DRIFT_THEME") {
	case "light":
		return false
	case "dark":
		return true
	}
	out, ok := w.(*os.File)
	if !ok {
		out = os.Stdout
	}
	return lipgloss.HasDarkBackground(os.Stdin, out)
}

func buildPalette(t *Theme) {
	ld := lipgloss.LightDark(t.Dark)

	accent := ld(lipgloss.Color("#0066cc"), lipgloss.Color("#5fafff"))
	success := ld(lipgloss.Color("#118800"), lipgloss.Color("#5fdf5f"))
	warn := ld(lipgloss.Color("#aa6600"), lipgloss.Color("#ffc56f"))
	errC := ld(lipgloss.Color("#cc0000"), lipgloss.Color("#ff6f6f"))
	dim := ld(lipgloss.Color("#888888"), lipgloss.Color("#888888"))
	muted := ld(lipgloss.Color("#555555"), lipgloss.Color("#bbbbbb"))

	t.AccentStyle = lipgloss.NewStyle().Foreground(accent)
	t.SuccessStyle = lipgloss.NewStyle().Foreground(success)
	t.WarnStyle = lipgloss.NewStyle().Foreground(warn)
	t.ErrorStyle = lipgloss.NewStyle().Foreground(errC).Bold(true)
	t.DimStyle = lipgloss.NewStyle().Foreground(dim)
	t.MutedStyle = lipgloss.NewStyle().Foreground(muted)
	t.BoldStyle = lipgloss.NewStyle().Bold(true)

	t.BorderFocused = lipgloss.NewStyle().Foreground(accent)
	t.BorderBlurred = lipgloss.NewStyle().Foreground(dim)

	t.KeyBindingStyle = lipgloss.NewStyle().Foreground(accent).Bold(true)
	t.KeyDescriptionStyle = lipgloss.NewStyle().Foreground(dim)
}

// String-form helpers mirror the legacy ui.Theme API.

func (t *Theme) Accent(s string) string  { return t.render(t.AccentStyle, s) }
func (t *Theme) Success(s string) string { return t.render(t.SuccessStyle, s) }
func (t *Theme) Warn(s string) string    { return t.render(t.WarnStyle, s) }
func (t *Theme) Error(s string) string   { return t.render(t.ErrorStyle, s) }
func (t *Theme) Dim(s string) string     { return t.render(t.DimStyle, s) }
func (t *Theme) Muted(s string) string   { return t.render(t.MutedStyle, s) }
func (t *Theme) Bold(s string) string    { return t.render(t.BoldStyle, s) }

func (t *Theme) render(st lipgloss.Style, s string) string {
	if t == nil || !t.Enabled {
		return s
	}
	return st.Render(s)
}
