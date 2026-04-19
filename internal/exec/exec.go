// Package exec is the single entry point every drift/lakitu caller uses to
// run an external process (ssh, mosh, docker, devpod, git). It enforces
// context cancellation, SIGTERM → SIGKILL escalation after WaitDelay, and
// never invokes a shell. Exit-code branching happens on the typed *Error.
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

const DefaultWaitDelay = 5 * time.Second

type Cmd struct {
	Name string
	Args []string
	Dir  string
	// Env: nil inherits the parent env; an empty non-nil slice means no env vars.
	Env       []string
	Stdin     io.Reader
	WaitDelay time.Duration
}

type Result struct {
	Stdout []byte
	Stderr []byte
	// ExitCode is always 0 — non-zero exits return *Error instead.
	ExitCode int
}

type Error struct {
	Name            string
	Args            []string
	ExitCode        int
	Stderr          []byte
	FirstStderrLine string
}

func (e *Error) Error() string {
	if e.FirstStderrLine != "" {
		return fmt.Sprintf("exec: %s exited %d: %s", e.Name, e.ExitCode, e.FirstStderrLine)
	}
	return fmt.Sprintf("exec: %s exited %d", e.Name, e.ExitCode)
}

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

	// Context cancellation wins over the child's signal-killed exit status
	// so callers can branch via errors.Is(err, context.Canceled).
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

	// Startup failures (program not found, exec permission denied, pipe
	// setup errors) land here. Wrap so errors.Is against os/exec sentinels
	// still works.
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
