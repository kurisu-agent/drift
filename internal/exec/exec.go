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
	// MirrorStdout / MirrorStderr, if non-nil, receive a live copy of the
	// respective stream while the internal buffers still accumulate for
	// the *Error tail. Used to stream subprocess output to an operator
	// under verbose mode. Two fields (rather than one) so SSH-RPC callers
	// can mirror stderr without stomping on the structured JSON response
	// on stdout. No ANSI stripping or redaction on this path — colors
	// pass through. The error-tail path runs its own redaction.
	MirrorStdout io.Writer
	MirrorStderr io.Writer
}

type Result struct {
	Stdout []byte
	Stderr []byte
}

type Error struct {
	Name            string
	Args            []string
	ExitCode        int
	Stderr          []byte
	Stdout          []byte
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

	applyCancelAndWaitDelay(c, DefaultWaitDelay)

	runErr := c.Run()
	return finishRun(ctx, bin, argv, runErr, nil, nil)
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
	if cmd.MirrorStdout != nil {
		c.Stdout = io.MultiWriter(&stdout, cmd.MirrorStdout)
	} else {
		c.Stdout = &stdout
	}
	if cmd.MirrorStderr != nil {
		c.Stderr = io.MultiWriter(&stderr, cmd.MirrorStderr)
	} else {
		c.Stderr = &stderr
	}

	applyCancelAndWaitDelay(c, cmd.WaitDelay)

	runErr := c.Run()
	if err := finishRun(ctx, cmd.Name, cmd.Args, runErr, stderr.Bytes(), stdout.Bytes()); err != nil {
		return Result{}, err
	}
	return Result{
		Stdout: stdout.Bytes(),
		Stderr: stderr.Bytes(),
	}, nil
}

// applyCancelAndWaitDelay wires the SIGTERM-on-cancel closure and sets a
// WaitDelay (falling back to DefaultWaitDelay on zero).
func applyCancelAndWaitDelay(c *osexec.Cmd, waitDelay time.Duration) {
	c.Cancel = func() error {
		if c.Process == nil {
			return errors.New("exec: Cancel called before process started")
		}
		return c.Process.Signal(syscall.SIGTERM)
	}
	if waitDelay == 0 {
		waitDelay = DefaultWaitDelay
	}
	c.WaitDelay = waitDelay
}

// finishRun maps c.Run()'s return into the package's typed error discipline:
// context cancellation wins over any child exit status, *osexec.ExitError
// becomes *Error, and anything else (startup failures) wraps with %s: %w.
// stderr/stdout are the captured buffers for Run's rich *Error; Interactive
// passes nil for both since stdio streams straight through.
func finishRun(ctx context.Context, name string, args []string, runErr error, stderr, stdout []byte) error {
	// Context cancellation wins over the child's signal-killed exit status
	// so callers can branch via errors.Is(err, context.Canceled).
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("exec: %s: %w", name, ctxErr)
	}

	if runErr == nil {
		return nil
	}

	var exitErr *osexec.ExitError
	if errors.As(runErr, &exitErr) {
		e := &Error{
			Name:     name,
			Args:     append([]string(nil), args...),
			ExitCode: exitErr.ExitCode(),
		}
		if stderr != nil || stdout != nil {
			e.Stderr = stderr
			e.Stdout = stdout
			e.FirstStderrLine = firstLine(stderr)
		}
		return e
	}

	// Startup failures (program not found, exec permission denied, pipe
	// setup errors) land here. Wrap so errors.Is against os/exec sentinels
	// still works.
	return fmt.Errorf("exec: %s: %w", name, runErr)
}

// StderrTail unwraps *Error and returns the trailing ~20 lines of captured
// stderr (capped at stderrTailMaxBytes), with ANSI escapes stripped and
// obvious secrets redacted. Returns "" if err carries no stderr. Callers
// attach the result via rpcerr.Error.With(DataKeyDevpodStderr, …) so it
// rides through JSON-RPC to the client for rendering.
func StderrTail(err error) string {
	var e *Error
	if !errors.As(err, &e) {
		return ""
	}
	return tailBytes(e.Stderr)
}

// StdoutTail mirrors StderrTail for captured stdout — needed because devpod
// writes its tunnelserver progress and most in-container failure detail to
// stdout, leaving stderr empty for the failures users actually care about.
func StdoutTail(err error) string {
	var e *Error
	if !errors.As(err, &e) {
		return ""
	}
	return tailBytes(e.Stdout)
}

func tailBytes(buf []byte) string {
	if len(buf) == 0 {
		return ""
	}
	s := stripANSI(string(buf))
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
	// Catches both `https://user:pass@host` and `https://<token>@host`
	// (the latter is what `git clone https://<pat>@github.com/…` uses).
	secretURLRE = regexp.MustCompile(`(https?://)[^/@\s]+@`)
	// secretGithubTokenRE matches GitHub's documented token prefixes
	// (fine-grained PAT, classic PAT, OAuth, server/user-to-server,
	// refresh) plus their body chars, so a token surviving outside any
	// URL anchor — e.g. devpod logging a parsed URL without its @host
	// portion — still gets scrubbed. Defense-in-depth: secretURLRE
	// catches the structural form, this catches the literal.
	secretGithubTokenRE = regexp.MustCompile(`gh[opsuir]_[A-Za-z0-9_]{16,}|github_pat_[A-Za-z0-9_]{16,}`)
)

func stripANSI(s string) string { return ansiRE.ReplaceAllString(s, "") }

// RedactSecrets is the public form of the same redaction stack the *Error
// tail uses (Authorization headers, KEY=VALUE secret pairs, embedded
// HTTPS credentials). Exposed so callers echoing argv to a verbose
// mirror can scrub each argument before printing.
func RedactSecrets(s string) string { return redactSecrets(s) }

// RedactingWriter line-buffers writes and runs each completed line
// through RedactSecrets before forwarding to w. ANSI escapes pass
// through (colors preserved). A trailing partial line (no `\n`)
// buffers until the next write completes it; for streaming subprocess
// output and one-line markers like `[kart] devpod up`, lines always
// terminate so the buffer drains naturally.
//
// Goroutine-unsafe: callers that share an instance across concurrent
// writers must wrap externally. The typical pattern is one wrapper per
// stream (e.g. one for stdout, one for stderr) so os/exec's copy
// goroutines don't race on the line buffer.
type RedactingWriter struct {
	W   io.Writer
	buf []byte
}

func (rw *RedactingWriter) Write(p []byte) (int, error) {
	rw.buf = append(rw.buf, p...)
	for {
		idx := bytes.IndexByte(rw.buf, '\n')
		if idx < 0 {
			break
		}
		line := rw.buf[:idx+1]
		if _, err := io.WriteString(rw.W, redactSecrets(string(line))); err != nil {
			return 0, err
		}
		rw.buf = rw.buf[idx+1:]
	}
	return len(p), nil
}

func redactSecrets(s string) string {
	s = secretAuthRE.ReplaceAllString(s, "${1}[REDACTED]")
	s = secretTokRE.ReplaceAllString(s, "${1}[REDACTED]")
	s = secretURLRE.ReplaceAllString(s, "${1}[REDACTED]@")
	s = secretGithubTokenRE.ReplaceAllString(s, "[REDACTED]")
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
