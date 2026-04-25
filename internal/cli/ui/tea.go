package ui

import (
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"syscall"

	tea "charm.land/bubbletea/v2"
)

// RunProgramOptions configures RunProgram.
type RunProgramOptions struct {
	Input   io.Reader   // defaults to os.Stdin
	Output  io.Writer   // defaults to os.Stdout
	Context context.Context
}

// RunProgram launches a tea.Program and blocks until it exits, the
// context is cancelled, or the user sends SIGINT. The returned Model is
// the final state of the program. Use this instead of constructing a
// tea.Program directly so signal/ctx wiring stays consistent across
// drift's interactive surfaces (dashboard, menu, future TUI commands).
func RunProgram(model tea.Model, o RunProgramOptions) (tea.Model, error) {
	if o.Output == nil {
		o.Output = os.Stdout
	}
	if o.Input == nil {
		o.Input = os.Stdin
	}
	ctx := o.Context
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	prog := tea.NewProgram(model,
		tea.WithContext(ctx),
		tea.WithInput(o.Input),
		tea.WithOutput(o.Output),
	)
	final, err := prog.Run()
	if err != nil && errors.Is(err, context.Canceled) {
		err = nil
	}
	return final, err
}

// AltScreenView is a small helper that wraps content into a tea.View
// with alt-screen mode enabled. Models running under the dashboard call
// this from their View() method so the program owns the full terminal.
func AltScreenView(content string) tea.View {
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}
