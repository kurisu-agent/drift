package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/kurisu-agent/drift/internal/config"
	"github.com/kurisu-agent/drift/internal/filebrowser"
	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/wire"
)

// BrowsePort is the fixed loopback port filebrowser listens on. Hardcoded
// (not negotiated) so the workstation can pre-print the URL and a `drift
// browse` interrupted before the RPC returns still leaves a known port to
// reattach to. 31337 — leet, distinctive, well above the registered range.
const BrowsePort = 31337

// browseStateFile records the running filebrowser process so a second
// `circuit.browse_start` reattaches instead of spawning a duplicate. The
// file lives under <driftHome>/run/ and is trimmed on stop.
type browseState struct {
	PID  int    `json:"pid"`
	Port int    `json:"port"`
	Root string `json:"root"`
}

// CircuitBrowseStartHandler spawns (or reuses) a filebrowser process
// rooted at the drift workspaces tree on the circuit. The process is
// session-detached via setsid so it outlives the `lakitu rpc` call
// that started it; the workstation then ssh-forwards a local port to
// BrowsePort and renders the URL.
//
// Lives lakitu-side because lakitu already knows where each kart's
// content directory lands on the host (devpod's agent layout, not a
// path the workstation can guess) and is the only place that can run
// one filebrowser across every kart's source. A workstation-side
// implementation would need to ssh into each kart, which is exactly
// the "drift would need to ssh into the kart" anti-pattern called out
// in CLAUDE.md.
func (d *Deps) CircuitBrowseStartHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p wire.CircuitBrowseStartParams
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	driftHome, err := d.driftHome()
	if err != nil {
		return nil, rpcerr.Internal("resolve drift home: %v", err).Wrap(err)
	}
	root, err := workspacesRoot()
	if err != nil {
		return nil, rpcerr.Internal("resolve workspaces root: %v", err).Wrap(err)
	}
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, rpcerr.Internal("mkdir workspaces root: %v", err).Wrap(err)
	}

	runDir := filepath.Join(driftHome, "run")
	if err := os.MkdirAll(runDir, 0o750); err != nil {
		return nil, rpcerr.Internal("mkdir run dir: %v", err).Wrap(err)
	}
	statePath := filepath.Join(runDir, "filebrowser.json")

	if state, ok := readBrowseState(statePath); ok && processAlive(state.PID) {
		return wire.CircuitBrowseStartResult{
			Port:           state.Port,
			Root:           state.Root,
			AlreadyRunning: true,
		}, nil
	}

	garage, gerr := d.garageDir()
	var token string
	if gerr == nil {
		t, terr := ResolveLakituGitHubAPIPAT(d.serverConfigPath(), garage, d.OpenChest)
		if terr != nil {
			// Don't block the bootstrap on a chest miss — fall through
			// unauthenticated. The download path will still work until
			// the IP gets rate-limited.
			fmt.Fprintf(os.Stderr, "warning: github_api_pat resolution failed (%v); fetching filebrowser unauthenticated\n", terr)
		}
		token = t
	}
	bin, err := filebrowser.EnsurePinned(ctx, driftHome, token)
	if err != nil {
		return nil, rpcerr.Internal("filebrowser bootstrap: %v", err).Wrap(err)
	}

	pid, err := spawnFilebrowser(bin, root, runDir, BrowsePort)
	if err != nil {
		return nil, rpcerr.Internal("spawn filebrowser: %v", err).Wrap(err)
	}
	if err := waitListening(BrowsePort, 5*time.Second); err != nil {
		// Spawned but never bound — kill the orphan and surface the timeout
		// so the operator sees the bootstrap log path instead of a silent
		// hang on the workstation.
		_ = killPID(pid)
		return nil, rpcerr.Internal("filebrowser did not start listening on %d: %v", BrowsePort, err).Wrap(err)
	}
	if err := writeBrowseState(statePath, browseState{PID: pid, Port: BrowsePort, Root: root}); err != nil {
		_ = killPID(pid)
		return nil, rpcerr.Internal("persist browse state: %v", err).Wrap(err)
	}
	return wire.CircuitBrowseStartResult{Port: BrowsePort, Root: root}, nil
}

// CircuitBrowseStopHandler kills the running filebrowser process (if
// any) and clears the state file. Idempotent: stopping a non-running
// browser returns Stopped=false without erroring, so `drift browse`
// can call this on every clean shutdown without first probing.
func (d *Deps) CircuitBrowseStopHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p wire.CircuitBrowseStopParams
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	driftHome, err := d.driftHome()
	if err != nil {
		return nil, rpcerr.Internal("resolve drift home: %v", err).Wrap(err)
	}
	statePath := filepath.Join(driftHome, "run", "filebrowser.json")
	state, ok := readBrowseState(statePath)
	if !ok {
		return wire.CircuitBrowseStopResult{Stopped: false}, nil
	}
	stopped := false
	if processAlive(state.PID) {
		_ = killPID(state.PID)
		stopped = true
	}
	_ = os.Remove(statePath)
	return wire.CircuitBrowseStopResult{Stopped: stopped}, nil
}

// RegisterCircuitBrowse wires both handlers into the registry. Mirrors
// RegisterKartConnect so tests can compose just these two RPCs.
func RegisterCircuitBrowse(reg *rpc.Registry, d *Deps) {
	reg.Register(wire.MethodCircuitBrowseStart, d.CircuitBrowseStartHandler)
	reg.Register(wire.MethodCircuitBrowseStop, d.CircuitBrowseStopHandler)
}

// workspacesRoot is the directory devpod bind-mounts kart content from.
// Layout: <DEVPOD_HOME>/agent/contexts/default/workspaces/<kart>/content/.
// Hardcoding the "default" context matches every other lakitu code path
// (drift only uses the default devpod context); a non-default context
// would be a policy change that touches more than just browse.
func workspacesRoot() (string, error) {
	home, err := config.DriftDevpodHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "agent", "contexts", "default", "workspaces"), nil
}

func spawnFilebrowser(bin, root, runDir string, port int) (int, error) {
	logPath := filepath.Join(runDir, "filebrowser.log")
	dbPath := filepath.Join(runDir, "filebrowser.db")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, fmt.Errorf("open log: %w", err)
	}
	defer func() { _ = logFile.Close() }()

	// --noauth keeps the quick-setup path: filebrowser writes a fresh
	// DB on first run with auth disabled. Re-run reuses the DB. The
	// listener binds 127.0.0.1 only — the workstation reaches it via
	// ssh -L, not directly, so opening it on 0.0.0.0 would just expand
	// the attack surface for nothing.
	cmd := exec.Command(bin,
		"--noauth",
		"-r", root,
		"-a", "127.0.0.1",
		"-p", strconv.Itoa(port),
		"-d", dbPath,
	)
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	// Capture the pid before Release — Release invalidates Process.Pid
	// (sets it to -1 on linux), and the state file needs the real pid so
	// circuit.browse_stop can find the orphan later.
	pid := cmd.Process.Pid
	// Release the child so the calling lakitu rpc process exits cleanly
	// without lingering as the parent. Setsid put it in its own session
	// already; this just closes our handle.
	_ = cmd.Process.Release()
	return pid, nil
}

func waitListening(port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("timeout")
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 doesn't actually deliver — it only checks deliverability,
	// which is the standard "is this PID alive and ours?" probe on unix.
	return proc.Signal(syscall.Signal(0)) == nil
}

func killPID(pid int) error {
	if pid <= 0 {
		return nil
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	// Negative PID targets the entire session — setsid put filebrowser
	// in its own session, so SIGTERM-ing the leader's session reaps any
	// children it forked. Falls back to leader-only if syscall.Kill on
	// the negative PID fails (rare on linux).
	if err := syscall.Kill(-pid, syscall.SIGTERM); err == nil {
		return nil
	}
	return proc.Signal(syscall.SIGTERM)
}

func readBrowseState(path string) (browseState, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return browseState{}, false
	}
	var state browseState
	if err := json.Unmarshal(data, &state); err != nil {
		return browseState{}, false
	}
	if state.PID <= 0 || state.Port <= 0 {
		return browseState{}, false
	}
	return state, true
}

func writeBrowseState(path string, state browseState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
