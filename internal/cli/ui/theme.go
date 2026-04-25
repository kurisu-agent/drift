package ui

import (
	"image/color"
	"io"
	"os"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/exp/charmtone"
)

// Theme is drift's resolved style tree. The flat *Style fields back the
// per-CLI helper methods (t.Dim, t.Accent, etc.) used across plain-output
// commands; the nested Border / Status groups back the dashboard rebrand
// and follow plan-16's brand guidelines wording (theme.Border.Focus,
// theme.Status.Success.Pill, ...).
//
// Build once at startup via NewTheme. Per-circuit accent overrides go
// through WithAccent which returns a copy with the borders + accent text
// re-tinted; the rest of the palette stays put.
type Theme struct {
	// Enabled toggles rendering. False under JSON / NO_COLOR / non-TTY.
	Enabled bool
	// Dark is true when the terminal background is dark.
	Dark bool
	// Profile is the detected color profile (NoTTY/ANSI/ANSI256/TrueColor).
	Profile colorprofile.Profile

	// AccentColor is the raw brand accent (Charple by default; per-circuit
	// override may swap it). Exposed for callers that compose their own
	// styles (status pills, custom borders, gradient endpoints).
	AccentColor color.Color
	// BgColor is the canvas background — Pepper on dark, Salt on light.
	BgColor color.Color

	// Flat style fields. Heavily used by the rest of the CLI through the
	// convenience methods below; kept stable for the rebrand.
	AccentStyle  lipgloss.Style
	SuccessStyle lipgloss.Style
	WarnStyle    lipgloss.Style
	ErrorStyle   lipgloss.Style
	DimStyle     lipgloss.Style
	MutedStyle   lipgloss.Style
	BoldStyle    lipgloss.Style

	// Border and Status are the structured style groups the dashboard
	// rebrand reaches for. Plan-16 brand guidelines use these names.
	Border Border
	Status Statuses
	Help   HelpStyles
}

// Border carries the two border tones the dashboard composes against.
// Focus is the brand accent (or per-circuit override) and lives on the
// active tab and any focused panel chrome. Subtle is the dim outline
// used for the outer dashboard border and any blurred panel.
type Border struct {
	Focus  lipgloss.Style
	Subtle lipgloss.Style
}

// Statuses bundles the four ambient status tones. Each tone exposes a
// Text style (foreground only, for inline icon+label) and a Pill style
// (bg + bold + padding 0/1, for scan-a-column status cells).
type Statuses struct {
	Success Status
	Warn    Status
	Error   Status
	Info    Status
}

// Status is one ambient status tone — text foreground plus pill chrome.
type Status struct {
	Text lipgloss.Style
	Pill lipgloss.Style
}

// HelpStyles is the thin wrapper bubbles/help reads from. The dashboard
// converts this into a help.Styles record at render time.
type HelpStyles struct {
	Key  lipgloss.Style
	Desc lipgloss.Style
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
		buildPalette(t, charmtone.Charple)
	}
	return t
}

// WithAccent returns a shallow copy of t with its accent recolored to c.
// Used by the dashboard when it's anchored to a single circuit and the
// circuit declares a per-circuit color tint. The non-accent palette
// stays put — only the focus border, accent text, and accent pill move.
func (t *Theme) WithAccent(c color.Color) *Theme {
	if t == nil || !t.Enabled || c == nil {
		return t
	}
	cp := *t
	buildPalette(&cp, c)
	return &cp
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

// buildPalette resolves every style on t against the charmtone catalog.
// accent is the brand accent — Charple by default, optionally a
// per-circuit override.
func buildPalette(t *Theme, accent color.Color) {
	ld := lipgloss.LightDark(t.Dark)

	// Text levels. Default is the body text color; muted is one step
	// down for labels and secondary content; subtle (DimStyle) is the
	// deepest gray, reserved for separators and hints.
	textDefault := ld(charmtone.Pepper, charmtone.Salt)
	textMuted := ld(charmtone.Iron, charmtone.Squid)
	textSubtle := ld(charmtone.Smoke, charmtone.Iron)
	bg := ld(charmtone.Salt, charmtone.Pepper)
	pillFg := ld(charmtone.Salt, charmtone.Pepper)

	// Status tones. Greens / yellows / reds / blues each get a
	// light/dark pair from charmtone's primary and tertiary palettes.
	successC := ld(charmtone.Pickle, charmtone.Julep)
	warnC := ld(charmtone.Tang, charmtone.Mustard)
	errorC := ld(charmtone.Sriracha, charmtone.Cherry)

	t.AccentColor = accent
	t.BgColor = bg

	t.AccentStyle = lipgloss.NewStyle().Foreground(accent)
	t.SuccessStyle = lipgloss.NewStyle().Foreground(successC)
	t.WarnStyle = lipgloss.NewStyle().Foreground(warnC)
	t.ErrorStyle = lipgloss.NewStyle().Foreground(errorC).Bold(true)
	t.DimStyle = lipgloss.NewStyle().Foreground(textSubtle)
	t.MutedStyle = lipgloss.NewStyle().Foreground(textMuted)
	t.BoldStyle = lipgloss.NewStyle().Bold(true).Foreground(textDefault)

	t.Border = Border{
		Focus:  lipgloss.NewStyle().Foreground(accent),
		Subtle: lipgloss.NewStyle().Foreground(textSubtle),
	}

	pill := func(c color.Color) Status {
		return Status{
			Text: lipgloss.NewStyle().Foreground(c),
			Pill: lipgloss.NewStyle().
				Bold(true).
				Padding(0, 1).
				Foreground(pillFg).
				Background(c),
		}
	}
	t.Status = Statuses{
		Success: pill(successC),
		Warn:    pill(warnC),
		Error:   pill(errorC),
		Info:    pill(accent),
	}

	t.Help = HelpStyles{
		Key:  lipgloss.NewStyle().Foreground(accent).Bold(true),
		Desc: lipgloss.NewStyle().Foreground(textMuted),
	}
}

// String-form helpers backed by the flat style fields. Used everywhere
// in plain-output drift commands. Stay terse so call sites read clean.

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
