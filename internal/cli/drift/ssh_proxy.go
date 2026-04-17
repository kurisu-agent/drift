package drift

import (
	"context"
	"errors"
	"fmt"
	osexec "os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/config"
	"github.com/kurisu-agent/drift/internal/name"
	"github.com/kurisu-agent/drift/internal/rpcerr"
)

// sshProxyCmd is the Kong command for `drift ssh-proxy <alias> <port>`.
// OpenSSH invokes it as the ProxyCommand for the wildcard `Host drift.*.*`
// block written by internal/sshconf. Users never run it directly — the Kong
// struct tag carries `hidden:""` so it doesn't show in help output.
type sshProxyCmd struct {
	Alias string `arg:"" help:"Per-kart SSH alias, e.g. drift.<circuit>.<kart>."`
	Port  string `arg:"" optional:"" help:"Destination port passed by ssh %p (unused; accepted for OpenSSH ProxyCommand compat)."`
}

// runSSHProxy parses the alias, resolves the circuit's managed SSH alias, and
// execs `ssh drift.<circuit> devpod ssh <kart> --stdio`, piping its stdio
// directly to our own. Exit code mirrors the child's so OpenSSH can diagnose
// transport failures correctly. See plans/PLAN.md § "How drift ssh-proxy
// works" for the full dance.
func runSSHProxy(ctx context.Context, io IO, _ *CLI, cmd sshProxyCmd, deps deps) int {
	circuit, kart, err := parseKartAlias(cmd.Alias)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}

	cfgPath, err := deps.clientConfigPath()
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	cfg, err := config.LoadClient(cfgPath)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	if _, ok := cfg.Circuits[circuit]; !ok {
		return errfmt.Emit(io.Stderr, rpcerr.NotFound(
			"circuit_not_found",
			"circuit %q in alias %q is not configured (run `drift circuit add %s`)",
			circuit, cmd.Alias, circuit,
		))
	}

	return execSSHProxy(ctx, io, "ssh", []string{"drift." + circuit, "devpod", "ssh", kart, "--stdio"})
}

// parseKartAlias extracts the circuit and kart names from
// `drift.<circuit>.<kart>`. Both names must satisfy the shared kart-name
// regex so invalid input fails fast with a clear message rather than a
// confusing downstream SSH error.
func parseKartAlias(alias string) (circuit, kart string, err error) {
	parts := strings.Split(alias, ".")
	if len(parts) != 3 || parts[0] != "drift" {
		return "", "", rpcerr.UserError(
			rpcerr.TypeInvalidFlag,
			"alias %q must look like drift.<circuit>.<kart>",
			alias,
		)
	}
	circuit, kart = parts[1], parts[2]
	if err := name.Validate("circuit", circuit); err != nil {
		return "", "", err
	}
	if err := name.Validate("kart", kart); err != nil {
		return "", "", err
	}
	return circuit, kart, nil
}

// execSSHProxy runs `bin argv...` with stdin/stdout/stderr wired directly to
// our own, mirrors the child's exit code, and enforces the SIGTERM → SIGKILL
// ladder from plans/PLAN.md § "Critical invariants".
//
// internal/exec.Run isn't suitable here because it captures stdio into
// buffers; ProxyCommand semantics require transparent pass-through.
func execSSHProxy(ctx context.Context, io IO, bin string, argv []string) int {
	c := osexec.CommandContext(ctx, bin, argv...)
	c.Stdin = io.Stdin
	c.Stdout = io.Stdout
	c.Stderr = io.Stderr
	c.Cancel = func() error { return c.Process.Signal(syscall.SIGTERM) }
	c.WaitDelay = 5 * time.Second

	err := c.Run()
	if err == nil {
		return 0
	}
	var ee *osexec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	// Transport failure before ssh produced an exit code (e.g. binary not
	// found). Surface the message and exit 1 — OpenSSH treats non-zero as a
	// ProxyCommand failure regardless of the specific code.
	fmt.Fprintln(io.Stderr, "drift ssh-proxy:", err.Error())
	return 1
}
