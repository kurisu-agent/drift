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
	"strings"
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
	ForwardAgent bool
	// SSHArgs are extra flags forwarded to ssh. On the ssh path they
	// slot between `-A` and the target host. On the mosh path they get
	// shell-quoted into `--ssh="ssh <args>"` so they apply to the ssh
	// invocation mosh uses to bootstrap `mosh-server`. Callers merge
	// config-pinned args with any CLI passthrough before populating this.
	SSHArgs          []string
	AutoStartTimeout time.Duration
	AutoStartPoll    time.Duration
}

func Run(ctx context.Context, d Deps, opts Options, stdio Stdio) error {
	d = withDefaults(d)

	if err := ensureRunning(ctx, d, opts); err != nil {
		return err
	}

	remote, err := fetchConnectArgv(ctx, d, opts)
	if err != nil {
		return err
	}

	useMosh := Transport(d.LookPath, opts.ForceSSH) == "mosh"
	bin, argv := buildConnectArgv(useMosh, opts, remote)
	if d.OnReady != nil {
		d.OnReady()
	}
	return d.Exec(ctx, bin, argv, stdio)
}

// fetchConnectArgv asks lakitu for the exact remote-command argv. On a
// circuit where kart.connect is registered the server returns a fully-
// baked stanza (`env DEVPOD_HOME=... /abs/devpod ssh <kart> --set-env
// KEY=VALUE ...`), so the client doesn't have to know the devpod binary
// path, the DEVPOD_HOME layout, or how to resolve session-env secrets.
//
// Older lakitu (pre-kart.connect) replies `method_not_found`; we fall
// back to the historic hand-built shape — `[devpod, ssh, kart]` plus a
// separate kart.session_env probe — so a drift client that's newer than
// its circuit still connects (just without the server-managed devpod
// path / DEVPOD_HOME injection). The stale-lakitu hint from
// internal/cli/drift/compat.go doesn't apply here because `drift
// connect`'s RPC is issued before the transport spawn; we handle the
// compat downshift locally instead.
func fetchConnectArgv(ctx context.Context, d Deps, opts Options) ([]string, error) {
	var res struct {
		Argv []string `json:"argv"`
	}
	err := d.Call(ctx, opts.Circuit, wire.MethodKartConnect,
		map[string]string{"name": opts.Kart}, &res)
	if err == nil {
		return res.Argv, nil
	}
	var rpcErr *rpcerr.Error
	if !errors.As(err, &rpcErr) || rpcErr.Type != "method_not_found" {
		return nil, err
	}
	// Legacy path: build the classic argv locally and re-ask for session env.
	sessionEnv, envErr := fetchSessionEnv(ctx, d, opts)
	if envErr != nil {
		return nil, envErr
	}
	remote := []string{"devpod", "ssh", opts.Kart}
	for _, kv := range sessionEnv {
		remote = append(remote, "--set-env", kv)
	}
	return remote, nil
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

func buildConnectArgv(useMosh bool, opts Options, remote []string) (string, []string) {
	target := "drift." + opts.Circuit
	// `remote` is whatever fetchConnectArgv produced — either the
	// server-resolved stanza (kart.connect, includes DEVPOD_HOME prefix
	// + --set-env pairs) or the legacy hand-built one. Either way we
	// only have to splice it into the transport argv here.
	if useMosh {
		// Build the mosh argv, then wrap the whole thing in `env -u LANG
		// -u LC_...` so mosh's perl wrapper sees no locale env vars and
		// doesn't forward them to the circuit via `-l KEY=VALUE`. The
		// forwarded locales are typically absent from the circuit's
		// glibc and produce `setlocale: cannot change locale (...)` noise
		// without adding value — the circuit's own defaults are what we
		// want anyway.
		moshArgv := []string{"mosh"}
		if len(opts.SSHArgs) > 0 {
			// mosh uses ssh to bootstrap mosh-server on the remote end;
			// --ssh="ssh <args>" swaps in our flag-ladened ssh invocation
			// for that bootstrap. mosh shell-splits the value, so each arg
			// is POSIX-single-quoted to survive paths with spaces or '.
			moshArgv = append(moshArgv, "--ssh="+BuildMoshSSHOverride(opts.SSHArgs))
		}
		moshArgv = append(moshArgv, target, "--")
		// Wrap `remote` in `script -qfc '<shell-quoted cmd>' /dev/null`.
		// mosh-server gives its child stdin=PTY but stdout/stderr=pipes
		// (so it can multiplex output over UDP); `devpod ssh` sees the
		// asymmetric fd state and declines to request a remote PTY,
		// leaving the container shell running on pipes (non-interactive
		// → zshrc's `[[ ! -o interactive ]]` guard returns → blank
		// screen). `script` allocates a real PTY across all three fds
		// before devpod inspects them, which propagates through to the
		// container shell.
		moshArgv = append(moshArgv, "script", "-qfc", shellQuoteArgs(remote), "/dev/null")

		return WrapMoshForLocaleStrip(moshArgv)
	}
	sshArgs := []string{"-t"}
	if opts.ForwardAgent {
		sshArgs = append(sshArgs, "-A")
	}
	// User-supplied ssh flags (config-pinned + CLI passthrough, already
	// merged by the caller) slot in before the target so they apply to
	// the connection, not the remote command. Last-wins options like -p
	// thus favor CLI over config; additive options like -i accumulate.
	sshArgs = append(sshArgs, opts.SSHArgs...)
	sshArgs = append(sshArgs, target)
	sshArgs = append(sshArgs, remote...)
	return "ssh", sshArgs
}

// moshLocaleStrip names the per-category locale vars to drop before
// exec-ing mosh so the perl wrapper doesn't forward a workstation-local
// value (e.g. `ja_JP.UTF-8`) the circuit's glibc probably lacks. LANG
// and LC_ALL are NOT on this list — those are force-set to `C.UTF-8`
// below, because mosh-client itself reads them to pick its terminal
// charset and falls back to US-ASCII (and refuses to run) when the
// only visible LC_* is the POSIX default.
var moshLocaleStrip = []string{
	"LANGUAGE",
	"LC_CTYPE", "LC_NUMERIC", "LC_TIME",
	"LC_COLLATE", "LC_MONETARY", "LC_MESSAGES",
	"LC_PAPER", "LC_NAME", "LC_ADDRESS",
	"LC_TELEPHONE", "LC_MEASUREMENT", "LC_IDENTIFICATION",
}

// moshLocaleForce names the locale vars we set on the mosh invocation.
// `C.UTF-8` is the most universally-available UTF-8 locale on modern
// glibc — mosh-client is happy (UTF-8 native), and on the server side
// LC_ALL takes precedence over any stragglers so the user doesn't see
// `setlocale: cannot change locale` spam even if workstation LC_*
// slipped through.
var moshLocaleForce = []string{
	"LANG=C.UTF-8",
	"LC_ALL=C.UTF-8",
}

// WrapMoshForLocaleStrip wraps a full `mosh <args>` argv so the process
// runs under `env -u <per-category LC_*> LANG=C.UTF-8 LC_ALL=C.UTF-8`.
// That neutralises the workstation's locale without leaving mosh-client
// in a POSIX/US-ASCII environment it refuses to run under. Callers pass
// the mosh argv with "mosh" as its first element; the helper returns
// (bin="env", argv=[…strip+force…, "mosh", …caller args]). Shared
// between the kart-connect path and the circuit-shell path so both
// exhibit the same locale behaviour.
func WrapMoshForLocaleStrip(moshArgv []string) (string, []string) {
	argv := make([]string, 0, 2*len(moshLocaleStrip)+len(moshLocaleForce)+len(moshArgv))
	for _, v := range moshLocaleStrip {
		argv = append(argv, "-u", v)
	}
	argv = append(argv, moshLocaleForce...)
	argv = append(argv, moshArgv...)
	return "env", argv
}

// BuildMoshSSHOverride assembles the value for mosh's --ssh=... flag:
// `ssh <arg1> <arg2> …`, each arg POSIX-single-quoted. Single quotes
// inside an arg close-escape-reopen via the standard `'\”` idiom.
func BuildMoshSSHOverride(args []string) string {
	parts := make([]string, 0, 1+len(args))
	parts = append(parts, "ssh")
	for _, a := range args {
		parts = append(parts, posixQuote(a))
	}
	return strings.Join(parts, " ")
}

// shellQuoteArgs joins args with POSIX single-quoting so the result is
// safe to hand to `sh -c` / `script -c`. Mirrors the quoting in
// BuildMoshSSHOverride but without the leading "ssh" token.
func shellQuoteArgs(args []string) string {
	parts := make([]string, 0, len(args))
	for _, a := range args {
		parts = append(parts, posixQuote(a))
	}
	return strings.Join(parts, " ")
}

// posixQuote wraps a single arg in POSIX single quotes, using the
// standard `'\”` idiom to embed literal single quotes.
func posixQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
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
			"kart %q is in state %q and cannot be connected to (try `drift kart logs %s`)",
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
