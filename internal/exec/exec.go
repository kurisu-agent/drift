// Package exec is the single entry point every drift/lakitu caller uses to
// run an external process (ssh, mosh, docker, devpod, git). It wraps
// os/exec with the following invariants (mechanically tested):
//
//   - exec.CommandContext is always used so cancellation of the parent
//     context tears the child down.
//   - Cmd.Cancel sends SIGTERM; Cmd.WaitDelay (default 5s) escalates to
//     SIGKILL for children that ignore SIGTERM.
//   - Argv is passed directly; no callers ever construct a shell line.
//     The accompanying source-grep test enforces the "no sh/bash" rule.
//   - Stdout and stderr are captured separately, and non-zero exits
//     surface as a typed *Error so callers can branch on exit code and
//     the first stderr line without re-parsing output.
package exec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	osexec "os/exec"
	"strings"
	"syscall"
	"time"
)

// DefaultWaitDelay is the grace period between SIGTERM and SIGKILL for a
// cancelled child.
const DefaultWaitDelay = 5 * time.Second

// Cmd describes a single subprocess invocation. The zero value is not
// useful — Name must be set. Args must NOT include the program name.
type Cmd struct {
	// Name is the program to run. Looked up on PATH by os/exec.
	Name string
	// Args is the argv tail (does not include Name). Passed verbatim to
	// the child; never interpreted by a shell.
	Args []string
	// Dir is the working directory; empty means inherit from the parent.
	Dir string
	// Env is the child environment. nil means inherit the parent's env;
	// an empty non-nil slice means run with no env vars.
	Env []string
	// Stdin is an optional reader wired to the child's stdin.
	Stdin io.Reader
	// WaitDelay is the SIGTERM→SIGKILL grace period. Zero falls back to
	// DefaultWaitDelay.
	WaitDelay time.Duration
}

// Result is the captured output of a successful run. On non-zero exit the
// caller receives an *Error instead; Result is only returned when the
// child exited with status 0.
type Result struct {
	Stdout []byte
	Stderr []byte
	// ExitCode is always 0 when Result is returned; it exists so callers
	// can pass the value through without branching on err==nil.
	ExitCode int
}

// Error is returned when the child exited non-zero. It carries the exit
// code, the full stderr bytes, and the first stderr line pre-extracted
// for use in human-readable error messages.
type Error struct {
	// Name is the program that was invoked.
	Name string
	// Args is the argv tail that was passed to the child. Useful for
	// diagnostics; callers should be careful not to log args that might
	// contain secrets.
	Args []string
	// ExitCode is the child's exit status. -1 when the process was
	// terminated by a signal.
	ExitCode int
	// Stderr is the full captured stderr.
	Stderr []byte
	// FirstStderrLine is the first non-empty line of Stderr, trimmed of
	// trailing whitespace. Empty when stderr was empty.
	FirstStderrLine string
}

func (e *Error) Error() string {
	if e.FirstStderrLine != "" {
		return fmt.Sprintf("exec: %s exited %d: %s", e.Name, e.ExitCode, e.FirstStderrLine)
	}
	return fmt.Sprintf("exec: %s exited %d", e.Name, e.ExitCode)
}

// Run launches cmd and blocks until it exits. On exit status 0 it returns
// a populated Result and a nil error. On non-zero exit it returns a
// zero Result and an *Error. If ctx is cancelled the child is sent
// SIGTERM, then SIGKILL after cmd.WaitDelay; Run waits for the child to
// be reaped and returns the context's error wrapped so errors.Is works
// against context.Canceled / context.DeadlineExceeded.
func Run(ctx context.Context, cmd Cmd) (Result, error) {
	if cmd.Name == "" {
		return Result{}, errors.New("exec: Cmd.Name is required")
	}

	c := osexec.CommandContext(ctx, cmd.Name, cmd.Args...)
	c.Dir = cmd.Dir
	c.Env = cmd.Env
	c.Stdin = cmd.Stdin

	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr

	c.Cancel = func() error {
		if c.Process == nil {
			return errors.New("exec: Cancel called before process started")
		}
		return c.Process.Signal(syscall.SIGTERM)
	}
	c.WaitDelay = cmd.WaitDelay
	if c.WaitDelay == 0 {
		c.WaitDelay = DefaultWaitDelay
	}

	runErr := c.Run()

	// Context cancellation takes precedence so callers can branch on it
	// via errors.Is even when the child exited with a signal-killed
	// status from our Cancel func.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return Result{}, fmt.Errorf("exec: %s: %w", cmd.Name, ctxErr)
	}

	if runErr == nil {
		return Result{
			Stdout:   stdout.Bytes(),
			Stderr:   stderr.Bytes(),
			ExitCode: 0,
		}, nil
	}

	var exitErr *osexec.ExitError
	if errors.As(runErr, &exitErr) {
		return Result{}, &Error{
			Name:            cmd.Name,
			Args:            append([]string(nil), cmd.Args...),
			ExitCode:        exitErr.ExitCode(),
			Stderr:          stderr.Bytes(),
			FirstStderrLine: firstLine(stderr.Bytes()),
		}
	}

	// Startup failures (program not found, permission denied on exec,
	// pipe setup errors) land here. Wrap so errors.Is against the
	// underlying os/exec sentinel still works.
	return Result{}, fmt.Errorf("exec: %s: %w", cmd.Name, runErr)
}

func firstLine(b []byte) string {
	for _, raw := range bytes.Split(b, []byte{'\n'}) {
		line := strings.TrimRight(string(raw), " \t\r")
		if line != "" {
			return line
		}
	}
	return ""
}
