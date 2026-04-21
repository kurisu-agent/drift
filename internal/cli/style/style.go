// Package style is drift's central palette for CLI text output. A Palette
// is a no-op when any of the following hold:
//
//   - jsonMode (callers pass root.Output == "json")
//   - the target Writer is not a TTY
//   - NO_COLOR is set in the environment
//
// All styled methods return the input unchanged in the no-op case, so call
// sites can stay unconditional: fmt.Fprintln(w, p.Accent(name)).
package style

import (
	"io"
	"os"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
)

type Palette struct {
	Enabled bool

	success lipgloss.Style
	warn    lipgloss.Style
	err     lipgloss.Style
	dim     lipgloss.Style
	accent  lipgloss.Style
	bold    lipgloss.Style
}

// For constructs a Palette appropriate for w. jsonMode short-circuits to
// a no-op palette so --output json never gains ANSI. Passing a non-TTY
// writer (e.g. a bytes.Buffer in tests) also yields a no-op palette.
func For(w io.Writer, jsonMode bool) *Palette {
	return &Palette{
		Enabled: shouldStyle(w, jsonMode),
		success: lipgloss.NewStyle().Foreground(lipgloss.Color("2")),
		warn:    lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
		err:     lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true),
		dim:     lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
		accent:  lipgloss.NewStyle().Foreground(lipgloss.Color("6")),
		bold:    lipgloss.NewStyle().Bold(true),
	}
}

func (p *Palette) Success(s string) string {
	if p == nil || !p.Enabled {
		return s
	}
	return p.success.Render(s)
}

func (p *Palette) Warn(s string) string {
	if p == nil || !p.Enabled {
		return s
	}
	return p.warn.Render(s)
}

func (p *Palette) Error(s string) string {
	if p == nil || !p.Enabled {
		return s
	}
	return p.err.Render(s)
}

func (p *Palette) Dim(s string) string {
	if p == nil || !p.Enabled {
		return s
	}
	return p.dim.Render(s)
}

func (p *Palette) Accent(s string) string {
	if p == nil || !p.Enabled {
		return s
	}
	return p.accent.Render(s)
}

func (p *Palette) Bold(s string) string {
	if p == nil || !p.Enabled {
		return s
	}
	return p.bold.Render(s)
}

func shouldStyle(w io.Writer, jsonMode bool) bool {
	if jsonMode {
		return false
	}
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}
