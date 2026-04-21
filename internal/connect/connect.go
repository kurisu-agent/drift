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
	"time"

	"github.com/kurisu-agent/drift/internal/devpod"
	driftexec "github.com/kurisu-agent/drift/internal/exec"
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
	Exec func(ctx context.Context, bin string, argv []string, stdio Stdio) error
	// OnReady fires once the kart is running, right before Exec takes the
	// TTY. CLI callers use it to stop a spinner so it doesn't fight the
	// interactive child for cursor control. nil skips the hook.
	OnReady func()
	Now     func() time.Time
	Sleep   func(d time.Duration)
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

	sessionEnv, err := fetchSessionEnv(ctx, d, opts)
	if err != nil {
		return err
	}

	useMosh := Transport(d.LookPath, opts.ForceSSH) == "mosh"
	bin, argv := buildConnectArgv(useMosh, opts, sessionEnv)
	if d.OnReady != nil {
		d.OnReady()
	}
	return d.Exec(ctx, bin, argv, stdio)
}

// fetchSessionEnv pulls resolved env.session KEY=VALUE pairs for the kart.
// Values resolve fresh on every call so rotated secrets show up on the
// next `drift connect` without a container restart. A circuit that
// predates this RPC (method_not_found) returns nil silently — the
// connect path stays forward-compatible.
func fetchSessionEnv(ctx context.Context, d Deps, opts Options) ([]string, error) {
	var res struct {
		Env []string `json:"env"`
	}
	if err := d.Call(ctx, opts.Circuit, wire.MethodKartSessionEnv,
		map[string]string{"name": opts.Kart}, &res); err != nil {
		var rpcErr *rpcerr.Error
		if errors.As(err, &rpcErr) && rpcErr.Type == "method_not_found" {
			return nil, nil
		}
		return nil, err
	}
	return res.Env, nil
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

// Transport reports which binary `drift connect` would shell out to given
// the same ForceSSH flag, without running anything. Returns "mosh" when
// mosh is on PATH and not suppressed, "ssh" otherwise. LookPath errors are
// swallowed and treated as "mosh not present" — matches the connect path's
// existing fallback so the user never sees a transport hint blocked on a
// detection failure.
func Transport(lookPath func(string) (string, error), forceSSH bool) string {
	if forceSSH {
		return "ssh"
	}
	if lookPath == nil {
		lookPath = osexec.LookPath
	}
	if _, err := lookPath("mosh"); err == nil {
		return "mosh"
	}
	return "ssh"
}

func buildConnectArgv(useMosh bool, opts Options, sessionEnv []string) (string, []string) {
	target := "drift." + opts.Circuit
	remote := []string{"devpod", "ssh", opts.Kart}
	// --set-env flags go on the remote devpod ssh invocation so the env
	// lives inside the ssh channel and dies with the session. Literal
	// values appear on the remote argv briefly; on the client they only
	// traverse the encrypted ssh transport.
	for _, kv := range sessionEnv {
		remote = append(remote, "--set-env", kv)
	}
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
	case devpod.StatusRunning:
		return nil
	case devpod.StatusStopped:
		var out map[string]any
		if err := d.Call(ctx, opts.Circuit, wire.MethodKartStart,
			map[string]string{"name": opts.Kart}, &out); err != nil {
			return err
		}
		return pollUntilRunning(ctx, d, opts)
	case devpod.StatusBusy:
		return pollUntilRunning(ctx, d, opts)
	default:
		return rpcerr.Conflict(
			"kart_not_connectable",
			"kart %q is in state %q and cannot be connected to (try `drift logs %s`)",
			opts.Kart, info.Status, opts.Kart,
		)
	}
}

// InfoResult is the narrow slice of kart.info that Run needs; Status uses
// the devpod enum so the switch in ensureRunning/pollUntilRunning is
// exhaustive at compile time. The underlying JSON string shape is
// unchanged — devpod.Status is a typed string.
type InfoResult struct {
	Status devpod.Status `json:"status"`
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
		switch info.Status {
		case devpod.StatusRunning:
			return nil
		case devpod.StatusBusy, devpod.StatusStopped:
			// fall through to the deadline/sleep tail below
		default:
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

// execStdio routes the interactive child through driftexec.Interactive so
// the Cancel/WaitDelay discipline matches the rest of drift. A non-zero
// exit surfaces as *ExitError so callers can propagate the remote session's
// status without the errfmt "error:" prefix.
func execStdio(ctx context.Context, bin string, argv []string, stdio Stdio) error {
	err := driftexec.Interactive(ctx, bin, argv, stdio.Stdin, stdio.Stdout, stdio.Stderr)
	if err == nil {
		return nil
	}
	var ee *driftexec.Error
	if errors.As(err, &ee) {
		return &ExitError{Code: ee.ExitCode}
	}
	return err
}

type ExitError struct{ Code int }

func (e *ExitError) Error() string { return fmt.Sprintf("remote session exited with code %d", e.Code) }
