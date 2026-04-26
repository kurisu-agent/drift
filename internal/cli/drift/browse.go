package drift

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	osexec "os/exec"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/wire"
)

type browseCmd struct {
	LocalPort int  `name:"local-port" short:"l" help:"Workstation port to bind (default: same as remote)."`
	NoStop    bool `name:"no-stop" help:"Leave filebrowser running on the circuit after the tunnel closes."`
}

// tunnelReadyTimeout bounds how long we wait for the ssh tunnel to bind
// the workstation port and reach filebrowser. ssh -L fails fast on
// every kind of misconfiguration (DNS, auth, port taken, lakitu down),
// so anything beyond a couple seconds is the tunnel actually being
// healthy. Kept short because the tunnel either works in <1s or not at
// all — a longer wait would just delay the error message.
const tunnelReadyTimeout = 3 * time.Second

// runBrowse starts (or re-attaches to) the circuit's filebrowser and
// holds an ssh -L tunnel open until the user Ctrl-Cs. Filebrowser runs
// server-side rooted at the drift workspaces tree so every kart's
// source is reachable from one URL — see CLAUDE.md "Client / server
// boundary" for why the heavy lifting lives on lakitu.
func runBrowse(ctx context.Context, ioPipes IO, root *CLI, cmd browseCmd, deps deps) int {
	_, circuit, err := resolveCircuit(root, deps)
	if err != nil {
		return errfmt.Emit(ioPipes.Stderr, err)
	}

	var startRes wire.CircuitBrowseStartResult
	if err := deps.call(ctx, circuit, wire.MethodCircuitBrowseStart,
		wire.CircuitBrowseStartParams{}, &startRes); err != nil {
		return errfmt.Emit(ioPipes.Stderr, err)
	}

	localPort := cmd.LocalPort
	if localPort == 0 {
		localPort = startRes.Port
	}

	// Hold the tunnel until the user Ctrl-Cs. ssh -N keeps the connection
	// open without spawning a remote shell; -L plumbs the workstation port
	// to the circuit's loopback filebrowser.
	//
	// `ControlPath=none` deliberately bypasses any ControlMaster the user
	// has configured for `drift.<circuit>`. With multiplexing enabled,
	// ssh hands the -L off to the existing master and exits immediately
	// — we need to stay in the foreground so the user's Ctrl-C reaches
	// us and we can tear down filebrowser. `ExitOnForwardFailure=yes`
	// turns a silently-skipped local bind (port already in use) into a
	// hard ssh exit, which our diagnostic can catch.
	tunnelCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	tunnel := osexec.CommandContext(tunnelCtx, "ssh",
		"-N",
		"-o", "ControlPath=none",
		"-o", "ExitOnForwardFailure=yes",
		"-L", fmt.Sprintf("%d:127.0.0.1:%d", localPort, startRes.Port),
		"drift."+circuit,
	)
	// Tee ssh's stderr so the user still sees connection diagnostics live
	// AND we can quote them back in the failure message — without the
	// buffer the "ssh tunnel collapsed" line would float without context.
	sshStderr := &lineBuffer{}
	tunnel.Stdout = ioPipes.Stderr
	tunnel.Stderr = io.MultiWriter(ioPipes.Stderr, sshStderr)
	startedAt := time.Now()
	if err := tunnel.Start(); err != nil {
		_ = stopBrowse(ctx, deps, circuit, cmd.NoStop)
		return errfmt.Emit(ioPipes.Stderr, fmt.Errorf("ssh -L: %w", err))
	}

	tunnelDone := make(chan error, 1)
	go func() { tunnelDone <- tunnel.Wait() }()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sig)

	// Confirm the tunnel actually established before printing the URL.
	// `ssh -N -L` dies fast on every kind of misconfiguration — if we
	// print "browsing http://..." first the user sees a URL flash and
	// the process exit with no explanation. Wait for either a successful
	// dial of localPort, an early ssh exit, or a brief timeout.
	sshExit, status := awaitTunnelReady(tunnelCtx, localPort, tunnelDone, tunnelReadyTimeout)
	switch {
	case errors.Is(status, errTunnelExited):
		_ = stopBrowse(ctx, deps, circuit, cmd.NoStop)
		return errfmt.Emit(ioPipes.Stderr,
			tunnelSetupError(circuit, localPort, startRes.Port, time.Since(startedAt), sshStderr.String(), sshExit))
	case status != nil:
		// Dial timed out but ssh is still alive — probably a slow
		// circuit, not a hard failure. Carry on; the user can still
		// hit Ctrl-C. Print a soft warning so the lag isn't mysterious.
		fmt.Fprintf(ioPipes.Stderr, "warning: tunnel not reachable after %s; the URL may take a moment\n", tunnelReadyTimeout)
	}

	statusLine := "browsing " + startRes.Root
	if startRes.AlreadyRunning {
		statusLine += " (reattached)"
	}
	fmt.Fprintln(ioPipes.Stderr, statusLine)
	fmt.Fprintln(ioPipes.Stderr, "  http://localhost:"+strconv.Itoa(localPort))
	fmt.Fprintln(ioPipes.Stderr, "  Ctrl-C to disconnect.")

	// Block until either the tunnel dies on its own or the user signals.
	// Either way we cancel ssh and call circuit.browse_stop unless
	// --no-stop. Track whether we cancelled so we can tell "user Ctrl-C'd
	// → ssh's non-zero exit is expected" apart from "ssh died on its own
	// → that's the actual failure". The earlier `isSignalKill(*ExitError)`
	// shortcut matched any exit code and ate real failures silently.
	var (
		tunnelErr       error
		userInterrupted bool
	)
	select {
	case <-sig:
		userInterrupted = true
		cancel()
		<-tunnelDone
	case tunnelErr = <-tunnelDone:
	}

	stopErr := stopBrowse(ctx, deps, circuit, cmd.NoStop)
	if stopErr != nil {
		fmt.Fprintf(ioPipes.Stderr, "warning: stop filebrowser: %v\n", stopErr)
	}

	if !userInterrupted {
		// ssh exited mid-session with no Ctrl-C from us. Surface the
		// diagnostic regardless of whether tunnel.Wait() returned an
		// error — `ssh -N` should never exit on its own, so even a clean
		// exit (tunnelErr == nil) is a real failure worth showing.
		return errfmt.Emit(ioPipes.Stderr,
			tunnelSetupError(circuit, localPort, startRes.Port, time.Since(startedAt), sshStderr.String(), tunnelErr))
	}
	return 0
}

// errTunnelExited reports that `ssh -N -L` exited before the tunnel
// became usable. The caller turns this into a tunnelSetupError with the
// captured ssh stderr; awaitTunnelReady itself doesn't know enough
// context to render the user-facing message.
var errTunnelExited = errors.New("ssh tunnel exited before becoming ready")

// awaitTunnelReady blocks until one of three things happens: a dial of
// localPort succeeds (tunnel is up), the ssh process exits early
// (returns errTunnelExited and the underlying exit error), or the
// timeout elapses (returns context.DeadlineExceeded). Polling beats a
// single dial because ssh's local bind and the remote channel come up
// a few ms apart — the first dial often races the bind.
//
// The first return value is whatever `tunnel.Wait()` produced when the
// process exited early; nil for the dial-success and timeout paths.
// We capture it here (rather than re-reading the channel later) so the
// caller's diagnostic can include the actual ssh exit status.
func awaitTunnelReady(ctx context.Context, localPort int, tunnelDone <-chan error, timeout time.Duration) (error, error) {
	deadline := time.Now().Add(timeout)
	tick := time.NewTicker(75 * time.Millisecond)
	defer tick.Stop()
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(localPort))
	for {
		select {
		case sshErr := <-tunnelDone:
			return sshErr, errTunnelExited
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil, nil
		}
		if time.Now().After(deadline) {
			return nil, context.DeadlineExceeded
		}
		select {
		case sshErr := <-tunnelDone:
			return sshErr, errTunnelExited
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-tick.C:
		}
	}
}

// tunnelSetupError renders the actionable diagnostic for an unexpected
// ssh exit. The captured stderr (often a one-line "Permission denied"
// or "Could not resolve hostname") is the most useful piece — the
// hint list covers the common silent-exit causes. sshExit is whatever
// `tunnel.Wait()` returned; it can be nil (`ssh -N` shouldn't ever
// exit cleanly, so even nil here is a real failure mode worth showing).
// Wrapped in an *rpcerr.Error so errfmt renders it with the same
// key:value layout as RPC failures.
func tunnelSetupError(circuit string, localPort, remotePort int, elapsed time.Duration, sshStderr string, sshExit error) error {
	hints := []string{
		fmt.Sprintf("workstation port %d may already be in use (try --local-port)", localPort),
		fmt.Sprintf("`ssh drift.%s true` should succeed (alias missing or auth failed?)", circuit),
		fmt.Sprintf("filebrowser may not be listening on the circuit at :%d (rebuild lakitu on the circuit)", remotePort),
	}
	exitDesc := "exited cleanly (unusual for ssh -N — likely backgrounded itself)"
	if sshExit != nil {
		exitDesc = sshExit.Error()
	}
	e := rpcerr.Internal("ssh tunnel to drift.%s collapsed after %s", circuit, elapsed.Round(time.Millisecond)).
		With("local_port", localPort).
		With("remote_port", remotePort).
		With("ssh_exit", exitDesc).
		With("hints", hints)
	if trimmed := lastNonEmptyLines(sshStderr, 8); trimmed != "" {
		e = e.With("ssh_stderr", trimmed)
	}
	return e
}

// lastNonEmptyLines keeps the last n non-blank lines of s. ssh's noise
// is almost always at the end (the actual error follows any "debug1:"
// or banner lines), and dumping the full stderr would bury the signal
// under unrelated chatter.
func lastNonEmptyLines(s string, n int) string {
	if s == "" {
		return ""
	}
	lines := bytes.Split(bytes.TrimRight([]byte(s), "\n"), []byte("\n"))
	keep := make([][]byte, 0, n)
	for i := len(lines) - 1; i >= 0 && len(keep) < n; i-- {
		if len(bytes.TrimSpace(lines[i])) == 0 {
			continue
		}
		keep = append([][]byte{lines[i]}, keep...)
	}
	return string(bytes.Join(keep, []byte("\n")))
}

// lineBuffer is a write-only sink that captures everything written to
// it. Mutex-guarded so the goroutine reading captured stderr doesn't
// race the writer goroutine that ssh's exec uses.
type lineBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lineBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lineBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func stopBrowse(ctx context.Context, deps deps, circuit string, noStop bool) error {
	if noStop {
		return nil
	}
	var res wire.CircuitBrowseStopResult
	return deps.call(ctx, circuit, wire.MethodCircuitBrowseStop,
		wire.CircuitBrowseStopParams{}, &res)
}

