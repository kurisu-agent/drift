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
	"regexp"
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

// Runner is the subprocess seam callers plumb through Deps/Client structs so
// tests can substitute a fake without spawning a real process. Production
// code binds DefaultRunner.
type Runner interface {
	Run(ctx context.Context, cmd Cmd) (Result, error)
}

// RunnerFunc adapts a plain function to Runner.
type RunnerFunc func(ctx context.Context, cmd Cmd) (Result, error)

func (f RunnerFunc) Run(ctx context.Context, cmd Cmd) (Result, error) { return f(ctx, cmd) }

// DefaultRunner is the production binding — a direct passthrough to Run.
var DefaultRunner Runner = RunnerFunc(Run)

// Interactive runs bin with argv and stdio wired straight through so the
// child owns the TTY. Uses the same Cancel/WaitDelay discipline as Run
// without buffering. Non-zero exit returns *Error (same type Run uses);
// startup failures return a plain error.
func Interactive(ctx context.Context, bin string, argv []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if bin == "" {
		return errors.New("exec: Interactive: bin is required")
	}

	execBin, execArgv := termuxLinkerWrap(bin, argv)
	c := osexec.CommandContext(ctx, execBin, execArgv...)
	c.Stdin = stdin
	c.Stdout = stdout
	c.Stderr = stderr

	c.Cancel = func() error {
		if c.Process == nil {
			return errors.New("exec: Cancel called before process started")
		}
		return c.Process.Signal(syscall.SIGTERM)
	}
	c.WaitDelay = DefaultWaitDelay

	runErr := c.Run()

	// Context cancellation wins over the child's signal-killed exit status
	// so callers can branch via errors.Is(err, context.Canceled).
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("exec: %s: %w", bin, ctxErr)
	}

	if runErr == nil {
		return nil
	}

	var exitErr *osexec.ExitError
	if errors.As(runErr, &exitErr) {
		return &Error{
			Name:     bin,
			Args:     append([]string(nil), argv...),
			ExitCode: exitErr.ExitCode(),
		}
	}

	// Startup failures (binary missing, exec permission denied) land here.
	return fmt.Errorf("exec: %s: %w", bin, runErr)
}

func Run(ctx context.Context, cmd Cmd) (Result, error) {
	if cmd.Name == "" {
		return Result{}, errors.New("exec: Cmd.Name is required")
	}

	execName, execArgs := termuxLinkerWrap(cmd.Name, cmd.Args)
	c := osexec.CommandContext(ctx, execName, execArgs...)
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

// StderrTail unwraps *Error and returns the trailing ~20 lines of captured
// stderr (capped at stderrTailMaxBytes), with ANSI escapes stripped and
// obvious secrets redacted. Returns "" if err carries no stderr. Callers
// attach the result via rpcerr.Error.With(DataKeyDevpodStderr, …) so it
// rides through JSON-RPC to the client for rendering.
func StderrTail(err error) string {
	var e *Error
	if !errors.As(err, &e) || len(e.Stderr) == 0 {
		return ""
	}
	s := stripANSI(string(e.Stderr))
	s = redactSecrets(s)
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > stderrTailMaxLines {
		lines = lines[len(lines)-stderrTailMaxLines:]
	}
	out := strings.Join(lines, "\n")
	if len(out) > stderrTailMaxBytes {
		// Drop the overflow from the front and align to the next newline so
		// we don't emit a half-line that confuses the reader.
		out = out[len(out)-stderrTailMaxBytes:]
		if idx := strings.IndexByte(out, '\n'); idx >= 0 {
			out = out[idx+1:]
		}
	}
	return out
}

const (
	stderrTailMaxLines = 20
	stderrTailMaxBytes = 4096
)

var (
	ansiRE       = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)
	secretAuthRE = regexp.MustCompile(`(?i)(authorization:\s*)\S+`)
	secretTokRE  = regexp.MustCompile(`(?i)((?:token|api[-_]?key|password|secret)=)[^\s&"']+`)
	secretURLRE  = regexp.MustCompile(`(https?://)[^:\s/@]+:[^@\s/]+@`)
)

func stripANSI(s string) string { return ansiRE.ReplaceAllString(s, "") }

func redactSecrets(s string) string {
	s = secretAuthRE.ReplaceAllString(s, "${1}[REDACTED]")
	s = secretTokRE.ReplaceAllString(s, "${1}[REDACTED]")
	s = secretURLRE.ReplaceAllString(s, "${1}[REDACTED]@")
	return s
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
