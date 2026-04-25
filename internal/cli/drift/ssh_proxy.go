package drift

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/config"
	driftexec "github.com/kurisu-agent/drift/internal/exec"
	"github.com/kurisu-agent/drift/internal/name"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/wire"
)

// sshProxyCmd is invoked by OpenSSH as the ProxyCommand for the wildcard
// `Host drift.*.*` block. Users never run it directly.
type sshProxyCmd struct {
	Alias string `arg:"" help:"Per-kart SSH alias, e.g. drift.<circuit>.<kart>."`
	Port  string `arg:"" optional:"" help:"Destination port passed by ssh %p (unused; accepted for OpenSSH ProxyCommand compat)."`
}

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
			rpcerr.TypeCircuitNotFound,
			"circuit %q in alias %q is not configured (run `drift circuit add %s`)",
			circuit, cmd.Alias, circuit,
		))
	}

	// Ask lakitu to build the same env-prefixed argv `drift connect`
	// would use, but in stdio-tunnel mode. Without this, a bare
	// `devpod ssh <kart> --stdio` runs without DEVPOD_HOME and devpod
	// fails to find lakitu's workspace tree under ~/.drift/devpod —
	// surfacing as ssh's cryptic "Connection closed by UNKNOWN port
	// 65535" the moment the proxy command exits.
	//
	// On any RPC failure (older lakitu without the method, kart not
	// known to the server yet, transient network blip) we fall back
	// to the legacy bare argv. The fallback path's own failure mode
	// is clearer than synthesising a proxy-level error and lets
	// integration shims that don't speak kart.connect still serve as
	// a transport.
	var res wire.KartConnectResult
	rpcErr := deps.call(ctx, circuit, wire.MethodKartConnect,
		wire.KartConnectParams{Name: kart, Stdio: true}, &res)
	argv := []string{"drift." + circuit}
	if rpcErr == nil {
		argv = append(argv, res.Argv...)
	} else {
		argv = append(argv, "devpod", "ssh", kart, "--stdio")
	}
	return execSSHProxy(ctx, io, "ssh", argv)
}

// parseKartAlias validates both names against the shared regex so invalid
// input fails fast, rather than producing a confusing downstream SSH error.
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

// execSSHProxy routes the interactive ssh child through driftexec.Interactive
// (Run buffers, which breaks ProxyCommand semantics). Non-zero ssh exits
// propagate as the process exit code; anything else is a transport failure
// and we log + exit 1 (OpenSSH treats any non-zero as ProxyCommand failure).
func execSSHProxy(ctx context.Context, io IO, bin string, argv []string) int {
	err := driftexec.Interactive(ctx, bin, argv, io.Stdin, io.Stdout, io.Stderr)
	if err == nil {
		return 0
	}
	var ee *driftexec.Error
	if errors.As(err, &ee) {
		return ee.ExitCode
	}
	fmt.Fprintln(io.Stderr, "drift ssh-proxy:", err.Error())
	return 1
}
