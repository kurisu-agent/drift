package drift

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/config"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/warmup"
	"golang.org/x/term"
)

type initCmd struct {
	SkipCircuits   bool `name:"skip-circuits" help:"Skip the circuit phase (assume already configured)."`
	SkipCharacters bool `name:"skip-characters" help:"Skip the character phase."`
	NoProbe        bool `name:"no-probe" help:"Skip the server.version probe (offline setup)."`
}

// runInit decides TTY-ness here so the library takes a plain bool —
// tests exercise both modes without spoofing fds. The underlying wizard
// library still lives in internal/warmup; only the user-facing CLI verb
// is `init`.
func runInit(ctx context.Context, io IO, root *CLI, cmd initCmd, deps deps) int {
	isTTY := stdinIsTTY(io.Stdin)
	opts := warmup.Options{
		SkipCircuits:   cmd.SkipCircuits,
		SkipCharacters: cmd.SkipCharacters,
		NoProbe:        cmd.NoProbe,
		IsTTY:          isTTY,
	}

	cfgPath, err := deps.clientConfigPath()
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}

	wdeps := warmup.Deps{
		LoadClientConfig: func() (*config.Client, error) { return config.LoadClient(cfgPath) },
		SaveClientConfig: func(c *config.Client) error { return config.SaveClient(cfgPath, c) },
		WriteSSHBlock: func(circuit, hostPart, userPart string) error {
			mgr, err := sshManagerFor(cfgPath)
			if err != nil {
				return err
			}
			return mgr.InstallCircuit(userSSHConfigPath(), circuit, hostPart, userPart)
		},
		Probe: func(ctx context.Context, circuit string) (*warmup.ProbeResult, error) {
			if deps.probe == nil {
				return nil, errors.New("probe not configured")
			}
			pr, err := deps.probe(ctx, circuit)
			if err != nil {
				return nil, err
			}
			return &warmup.ProbeResult{Version: pr.Version, API: pr.API, LatencyMS: pr.LatencyMS}, nil
		},
		ProbeInfo: deps.probeInfo,
		Call:      deps.call,
		Now:       time.Now,
	}

	err = warmup.Run(ctx, opts, wdeps, io.Stdin, io.Stdout)
	if err == nil {
		return 0
	}
	var re *rpcerr.Error
	if errors.As(err, &re) {
		fmt.Fprintf(io.Stderr, "error: %s\n", re.Message)
		return int(re.Code)
	}
	return errfmt.Emit(io.Stderr, err)
}

// isTTY returns true only when fd is a *os.File backed by an actual
// terminal. An earlier version leaned on os.ModeCharDevice to avoid
// pulling in golang.org/x/term, but that also matches /dev/null — the
// file Go's os/exec hands a child when Stdin/Stdout are nil — so every
// non-interactive caller (CI runners, systemd, cron) was being
// misclassified as interactive and features like drift-new auto-connect
// would fire by accident. term.IsTerminal does a proper TIOCGWINSZ
// ioctl and distinguishes a TTY from /dev/null.
func isTTY(fd any) bool {
	f, ok := fd.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd())) //nolint:gosec // G115: posix file descriptors always fit in int
}

// stdinIsTTY / stdoutIsTTY are thin aliases kept for call-site clarity.
func stdinIsTTY(r any) bool  { return isTTY(r) }
func stdoutIsTTY(w any) bool { return isTTY(w) }
