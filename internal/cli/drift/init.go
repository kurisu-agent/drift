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
	"github.com/kurisu-agent/drift/internal/tailscale"
	"github.com/kurisu-agent/drift/internal/warmup"
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
			if err := mgr.EnsureInclude(userSSHConfigPath()); err != nil {
				return err
			}
			if err := mgr.EnsureSocketsDir(); err != nil {
				return err
			}
			if err := mgr.WriteCircuitBlock(circuit, hostPart, userPart); err != nil {
				return err
			}
			return mgr.EnsureWildcardBlock()
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

	// Only hand the warmup library a tailscale picker when the binary is
	// actually on PATH — library stays pure, detection lives at the CLI
	// boundary.
	if tailscale.Available() {
		wdeps.TailscalePicker = func(ctx context.Context) (string, bool, error) {
			return tailscalePicker(ctx, io.Stdin, io.Stderr)
		}
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

// stdinIsTTY: avoid pulling golang.org/x/term for one call — *os.File
// mode check covers files/pipes/TTYs. Non-*os.File readers (bytes.Buffer
// in unit tests) are treated as non-TTY; those tests drive the library
// directly with IsTTY set explicitly.
func stdinIsTTY(r any) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}
