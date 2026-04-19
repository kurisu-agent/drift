// Package connect implements the transport-selection and auto-start logic
// behind `drift connect <kart>`. Separated from internal/cli/drift/connect.go
// so the decision logic is unit-testable without a Kong harness.
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

type Deps struct {
	// LookPath mirrors os/exec.LookPath for mosh detection.
	LookPath func(file string) (string, error)
	// Call signature matches internal/rpc/client.Call so prod binds it as
	// `deps.Call = client.Call`.
	Call func(ctx context.Context, circuit, method string, params, result any) error
	// Exec defaults to execStdio — a direct os/exec Run with stdio wired
	// through so the user's terminal sees the child transparently.
	Exec  func(ctx context.Context, bin string, argv []string, stdio Stdio) error
	Now   func() time.Time
	Sleep func(d time.Duration)
}

type Stdio struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

type Options struct {
	// Circuit is the short name; Run passes "drift.<Circuit>" to mosh/ssh.
	Circuit string
	Kart    string
	// ForceSSH skips mosh detection entirely.
	ForceSSH bool
	// ForwardAgent sets `-A` on the ssh fallback (no effect on mosh).
	ForwardAgent     bool
	AutoStartTimeout time.Duration
	AutoStartPoll    time.Duration
}

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

func ensureRunning(ctx context.Context, d Deps, opts Options) error {
	info, err := fetchInfo(ctx, d, opts)
	if err != nil {
		return err
	}
	switch info.Status {
	case "running":
		return nil
	case "stopped":
		var out map[string]any
		if err := d.Call(ctx, opts.Circuit, wire.MethodKartStart,
			map[string]string{"name": opts.Kart}, &out); err != nil {
			return err
		}
		return pollUntilRunning(ctx, d, opts)
	case "busy":
		return pollUntilRunning(ctx, d, opts)
	default:
		return rpcerr.Conflict(
			"kart_not_connectable",
			"kart %q is in state %q and cannot be connected to (try `drift logs %s`)",
			opts.Kart, info.Status, opts.Kart,
		)
	}
}

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

// execStdio wires stdio straight through so the child owns the TTY.
// internal/exec.Run is deliberately bypassed because it buffers — wrong for
// an interactive session. Cancel/WaitDelay reproduce the discipline inline.
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
		// Propagate the child's exit code so the shell sees what the remote
		// session produced, rather than a drift-level failure.
		return &ExitError{Code: ee.ExitCode()}
	}
	return err
}

type ExitError struct{ Code int }

func (e *ExitError) Error() string { return fmt.Sprintf("remote session exited with code %d", e.Code) }
