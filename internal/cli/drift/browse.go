package drift

import (
	"context"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	driftexec "github.com/kurisu-agent/drift/internal/exec"
	"github.com/kurisu-agent/drift/internal/wire"
)

type browseCmd struct {
	LocalPort int  `name:"local-port" short:"l" help:"Workstation port to bind (default: same as remote)."`
	NoStop    bool `name:"no-stop" help:"Leave filebrowser running on the circuit after the tunnel closes."`
}

// runBrowse starts (or re-attaches to) the circuit's filebrowser and
// holds an ssh -L tunnel open until the user Ctrl-Cs. Filebrowser runs
// server-side rooted at the drift workspaces tree so every kart's
// source is reachable from one URL — see CLAUDE.md "Client / server
// boundary" for why the heavy lifting lives on lakitu.
func runBrowse(ctx context.Context, io IO, root *CLI, cmd browseCmd, deps deps) int {
	_, circuit, err := resolveCircuit(root, deps)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}

	var startRes wire.CircuitBrowseStartResult
	if err := deps.call(ctx, circuit, wire.MethodCircuitBrowseStart,
		wire.CircuitBrowseStartParams{}, &startRes); err != nil {
		return errfmt.Emit(io.Stderr, err)
	}

	localPort := cmd.LocalPort
	if localPort == 0 {
		localPort = startRes.Port
	}

	// Hold the tunnel until the user Ctrl-Cs. ssh -N keeps the connection
	// open without spawning a remote shell; -L plumbs the workstation port
	// to the circuit's loopback filebrowser. Using the drift-managed
	// `drift.<circuit>` alias means the tunnel inherits the same
	// ControlMaster / ProxyCommand the rest of drift uses to reach lakitu.
	tunnelCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	tunnel := osexec.CommandContext(tunnelCtx, "ssh",
		"-N",
		"-L", fmt.Sprintf("%d:127.0.0.1:%d", localPort, startRes.Port),
		"drift."+circuit,
	)
	tunnel.Stdout = io.Stderr
	tunnel.Stderr = io.Stderr
	if err := tunnel.Start(); err != nil {
		_ = stopBrowse(ctx, deps, circuit, cmd.NoStop)
		return errfmt.Emit(io.Stderr, fmt.Errorf("ssh -L: %w", err))
	}

	statusLine := "browsing " + startRes.Root
	if startRes.AlreadyRunning {
		statusLine += " (reattached)"
	}
	fmt.Fprintln(io.Stderr, statusLine)
	fmt.Fprintln(io.Stderr, "  http://127.0.0.1:"+strconv.Itoa(localPort))
	fmt.Fprintln(io.Stderr, "  Ctrl-C to disconnect.")

	// Block until either the tunnel dies on its own or the user signals.
	// Either way we cancel ssh and call circuit.browse_stop unless
	// --no-stop. Selecting on tunnelDone catches "ssh failed immediately"
	// (e.g. local port already bound) without leaving filebrowser
	// orphaned on the circuit.
	tunnelDone := make(chan error, 1)
	go func() { tunnelDone <- tunnel.Wait() }()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sig)

	var tunnelErr error
	select {
	case <-sig:
		cancel()
		<-tunnelDone
	case tunnelErr = <-tunnelDone:
	}

	stopErr := stopBrowse(ctx, deps, circuit, cmd.NoStop)

	if tunnelErr != nil && !isSignalKill(tunnelErr) {
		fmt.Fprintf(io.Stderr, "warning: ssh tunnel: %v\n", tunnelErr)
	}
	if stopErr != nil {
		fmt.Fprintf(io.Stderr, "warning: stop filebrowser: %v\n", stopErr)
	}
	return 0
}

func stopBrowse(ctx context.Context, deps deps, circuit string, noStop bool) error {
	if noStop {
		return nil
	}
	var res wire.CircuitBrowseStopResult
	return deps.call(ctx, circuit, wire.MethodCircuitBrowseStop,
		wire.CircuitBrowseStopParams{}, &res)
}

// isSignalKill reports whether the error is the expected "we cancelled
// ssh" outcome. Both context cancellation and SIGTERM-on-exit show up
// as exit-code !=0; the user doesn't need to see a warning about a
// process they themselves killed.
func isSignalKill(err error) bool {
	if errors.Is(err, context.Canceled) {
		return true
	}
	var ee *osexec.ExitError
	if errors.As(err, &ee) {
		return true
	}
	var de *driftexec.Error
	return errors.As(err, &de)
}
