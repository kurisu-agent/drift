// Package ui is drift's unified presentation layer. Every user-facing
// renderer (colors, tables, spinners, prompts, headers) flows through here
// so subcommands stop reaching for raw lipgloss/bubbletea/huh directly.
package ui

import (
	"io"
	"os"

	"github.com/mattn/go-isatty"
)

// Mode describes how a command should render output.
type Mode int

const (
	// ModeJSON emits JSON to stdout. No spinner, no color, no tea program.
	ModeJSON Mode = iota
	// ModePlain emits line-based stdout. No ANSI, no spinner. Pipes, CI, NO_COLOR, non-TTY.
	ModePlain
	// ModeColor is a TTY with color and a spinner, but no bubbletea program.
	ModeColor
	// ModeTUI is a TTY running a full bubbletea program with alt-screen.
	ModeTUI
)

func (m Mode) String() string {
	switch m {
	case ModeJSON:
		return "json"
	case ModePlain:
		return "plain"
	case ModeColor:
		return "color"
	case ModeTUI:
		return "tui"
	}
	return "unknown"
}

// IsColor reports whether the mode emits ANSI color.
func (m Mode) IsColor() bool { return m == ModeColor || m == ModeTUI }

// IsTTY reports whether the mode is a TTY-bound mode.
func (m Mode) IsTTY() bool { return m == ModeColor || m == ModeTUI }

// ModeFlags is the input to DetectMode. Each field corresponds to a CLI
// flag or environment signal that overrides automatic detection.
type ModeFlags struct {
	JSON    bool
	NoTUI   bool
	NoColor bool
	Debug   bool
	// WantTUI is a hint that the caller wants the full bubbletea path
	// (e.g. drift dashboard). When false the resolver caps at ModeColor.
	WantTUI bool
}

// DetectMode resolves the rendering mode for a command. stdout is the
// primary user-facing writer; stderr is consulted only as a fallback for
// TTY detection on commands that emit JSON to stdout.
func DetectMode(stdout, stderr io.Writer, f ModeFlags) Mode {
	if f.JSON {
		return ModeJSON
	}
	if f.NoColor || os.Getenv("NO_COLOR") != "" {
		// NO_COLOR forces plain — no ANSI, no TUI, regardless of TTY.
		return ModePlain
	}
	stdoutTTY := isTTY(stdout)
	if !stdoutTTY {
		return ModePlain
	}
	if f.NoTUI || os.Getenv("DRIFT_NO_TUI") != "" || f.Debug {
		return ModeColor
	}
	if f.WantTUI {
		return ModeTUI
	}
	return ModeColor
}

func isTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isFileTTY(f)
}

func isFileTTY(f *os.File) bool {
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}
