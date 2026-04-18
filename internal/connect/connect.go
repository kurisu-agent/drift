// Package connect implements the transport-selection and auto-start logic
// behind `drift connect <kart>`. It is deliberately narrow: the caller
// supplies a [Deps] bundle carrying the RPC client + subprocess hooks, and
// Run decides between mosh and ssh, waits for the kart to reach `running`
// if necessary, and execs into the final command.
//
// Separating this from internal/cli/drift/connect.go keeps the decision
// logic unit-testable without a Kong harness.
package connect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	osexec "os/exec"
	"syscall"
	"time"

	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/wire"
)

// Deps captures the side-effecting collaborators Run needs. Every field is
// an injection point — production binds them via closure over the real
// exec.LookPath / rpc client / syscall.Exec; tests bind stubs.
type Deps struct {
	// LookPath mirrors os/exec.LookPath for mosh detection. Stubs can
	// return err to force the ssh fallback even on a host that has mosh.
	LookPath func(file string) (string, error)
	// Call issues an RPC to the server. The signature matches
	// internal/rpc/client.Call so prod can bind it as
	// `deps.Call = client.Call`.
	Call func(ctx context.Context, circuit, method string, params, result any) error
	// Exec is how Run hands control to mosh or ssh. In production this is a
	// direct os/exec Run with stdin/stdout/stderr wired through, so the
	// user's terminal sees the child process transparently. The default is
	// execStdio — tests override it to capture argv.
	Exec func(ctx context.Context, bin string, argv []string, stdio Stdio) error
	// Now is used by the auto-start polling loop so tests can drive time
	// without waiting for real seconds.
	Now func() time.Time
	// Sleep is the polling backoff primitive.
	Sleep func(d time.Duration)
}

// Stdio is the passthrough bundle exec receives.
type Stdio struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Options controls a single drift connect invocation.
type Options struct {
	// Circuit is the drift-managed circuit alias — the thing that appears
	// after "drift." in the generated SSH config. Run will pass the alias
	// "drift.<Circuit>" to mosh/ssh.
	Circuit string
	// Kart is the name of the devcontainer workspace on the circuit.
	Kart string
	// ForceSSH skips mosh detection entirely.
	ForceSSH bool
	// ForwardAgent sets `-A` on the ssh fallback (no effect on mosh).
	ForwardAgent bool
	// AutoStartTimeout bounds how long Run waits for a stopped kart to
	// reach `running` after sending kart.start. Zero defaults to 30s.
	AutoStartTimeout time.Duration
	// AutoStartPoll is the poll interval during the running-state wait.
	// Zero defaults to 500ms.
	AutoStartPoll time.Duration
}

// Run does everything `drift connect` needs: checks status, triggers
// kart.start if stopped, picks mosh vs ssh, and execs the final command.
// The returned error is already shaped for errfmt.Emit — rpcerr-typed for
// RPC failures, plain error for exec transport issues.
func Run(ctx context.Context, d Deps, opts Options, stdio Stdio) error {
	d = withDefaults(d)

	if err := ensureRunning(ctx, d, opts); err != nil {
		return err
	}

	useMosh := !opts.ForceSSH && moshAvailable(d)
	bin, argv := buildConnectArgv(useMosh, opts)
	return d.Exec(ctx, bin, argv, stdio)
}

func withDefaults(d Deps) Deps {
	if d.LookPath == nil {
		d.LookPath = osexec.LookPath
	}
	if d.Exec == nil {
		d.Exec = execStdio
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Sleep == nil {
		d.Sleep = time.Sleep
	}
	return d
}

func moshAvailable(d Deps) bool {
	if d.LookPath == nil {
		return false
	}
	_, err := d.LookPath("mosh")
	return err == nil
}

// buildConnectArgv constructs the command to exec. Mosh path puts
// `devpod ssh <kart>` after the `--` separator so mosh hands it straight to
// the remote shell; ssh path invokes the same command via `ssh -t`.
func buildConnectArgv(useMosh bool, opts Options) (string, []string) {
	target := "drift." + opts.Circuit
	remote := []string{"devpod", "ssh", opts.Kart}
	if useMosh {
		argv := append([]string{target, "--"}, remote...)
		return "mosh", argv
	}
	sshArgs := []string{"-t"}
	if opts.ForwardAgent {
		sshArgs = append(sshArgs, "-A")
	}
	sshArgs = append(sshArgs, target)
	sshArgs = append(sshArgs, remote...)
	return "ssh", sshArgs
}

// ensureRunning checks the kart's current status via kart.info and, if
// stopped, issues kart.start and polls kart.info until status == "running"
// or the timeout elapses. Any non-stopped/running terminal state
// (stale_kart, error, not_found) returns the rpcerr as-is so errfmt shows
// the typed error to the user.
func ensureRunning(ctx context.Context, d Deps, opts Options) error {
	info, err := fetchInfo(ctx, d, opts)
	if err != nil {
		return err
	}
	switch info.Status {
	case "running":
		return nil
	case "stopped":
		// Fire kart.start, then poll until running.
		var out map[string]any
		if err := d.Call(ctx, opts.Circuit, wire.MethodKartStart,
			map[string]string{"name": opts.Kart}, &out); err != nil {
			return err
		}
		return pollUntilRunning(ctx, d, opts)
	case "busy":
		// A transient state; poll as if a start is already in flight.
		return pollUntilRunning(ctx, d, opts)
	default:
		return rpcerr.Conflict(
			"kart_not_connectable",
			"kart %q is in state %q and cannot be connected to (try `drift logs %s`)",
			opts.Kart, info.Status, opts.Kart,
		)
	}
}

// InfoResult is a minimal view of kart.info — only the status field matters
// for connect's state machine. We tolerate unknown fields because the
// kart.info schema is additive-forward.
type InfoResult struct {
	Status string `json:"status"`
}

func fetchInfo(ctx context.Context, d Deps, opts Options) (InfoResult, error) {
	var out json.RawMessage
	if err := d.Call(ctx, opts.Circuit, wire.MethodKartInfo,
		map[string]string{"name": opts.Kart}, &out); err != nil {
		return InfoResult{}, err
	}
	var info InfoResult
	if err := json.Unmarshal(out, &info); err != nil {
		return InfoResult{}, fmt.Errorf("parse kart.info response: %w", err)
	}
	return info, nil
}

func pollUntilRunning(ctx context.Context, d Deps, opts Options) error {
	timeout := opts.AutoStartTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	poll := opts.AutoStartPoll
	if poll == 0 {
		poll = 500 * time.Millisecond
	}
	deadline := d.Now().Add(timeout)
	for {
		info, err := fetchInfo(ctx, d, opts)
		if err != nil {
			return err
		}
		if info.Status == "running" {
			return nil
		}
		if info.Status != "busy" && info.Status != "stopped" {
			return rpcerr.Conflict(
				"kart_not_connectable",
				"kart %q reached state %q while waiting for running",
				opts.Kart, info.Status,
			)
		}
		if !d.Now().Before(deadline) {
			return rpcerr.New(
				rpcerr.CodeConflict,
				"kart_autostart_timeout",
				"kart %q did not reach running within %s",
				opts.Kart, timeout,
			)
		}
		d.Sleep(poll)
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

// execStdio is the production Exec binding: stdin/stdout/stderr are wired
// straight through so the child owns the TTY. We keep the SIGTERM → SIGKILL
// ladder by setting Cmd.Cancel and Cmd.WaitDelay; internal/exec.Run would
// buffer stdio, which is wrong for an interactive session.
func execStdio(ctx context.Context, bin string, argv []string, stdio Stdio) error {
	c := osexec.CommandContext(ctx, bin, argv...)
	c.Stdin = stdio.Stdin
	c.Stdout = stdio.Stdout
	c.Stderr = stdio.Stderr
	c.Cancel = func() error { return c.Process.Signal(syscall.SIGTERM) }
	c.WaitDelay = 5 * time.Second
	err := c.Run()
	if err == nil {
		return nil
	}
	var ee *osexec.ExitError
	if errors.As(err, &ee) {
		// Surface the child's exit code to drift's top-level so the shell
		// sees the same exit code the remote session produced.
		return &ExitError{Code: ee.ExitCode()}
	}
	return err
}

// ExitError lets the caller convey the child's exit code to os.Exit without
// mistaking a non-zero exit for a drift-level failure. The message reuses
// the child's fmt so errfmt.Emit doesn't label a clean ssh exit as an error.
type ExitError struct{ Code int }

func (e *ExitError) Error() string { return fmt.Sprintf("remote session exited with code %d", e.Code) }
