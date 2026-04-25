package ui

import (
	"io"
	"os"
)

// Surface bundles a Theme, the resolved Mode, and the writer set for a
// single command invocation. New code should accept a *Surface; the
// legacy callers using *Theme directly continue to compile via NewTheme.
type Surface struct {
	Mode  Mode
	Theme *Theme

	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader
}

// SurfaceOptions controls Surface construction.
type SurfaceOptions struct {
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader
	Flags  ModeFlags
}

// NewSurface builds a Surface for the current invocation. nil writers
// default to the os streams.
func NewSurface(o SurfaceOptions) *Surface {
	if o.Stdout == nil {
		o.Stdout = os.Stdout
	}
	if o.Stderr == nil {
		o.Stderr = os.Stderr
	}
	if o.Stdin == nil {
		o.Stdin = os.Stdin
	}
	mode := DetectMode(o.Stdout, o.Stderr, o.Flags)
	jsonMode := mode == ModeJSON
	return &Surface{
		Mode:   mode,
		Theme:  NewTheme(o.Stdout, jsonMode),
		Stdout: o.Stdout,
		Stderr: o.Stderr,
		Stdin:  o.Stdin,
	}
}

// IsInteractive reports whether the surface can prompt for input
// (i.e. stdin is a TTY and stdout is a TTY).
func (s *Surface) IsInteractive() bool {
	if s.Mode == ModePlain || s.Mode == ModeJSON {
		return false
	}
	f, ok := s.Stdin.(*os.File)
	if !ok {
		return false
	}
	return isFileTTY(f)
}
