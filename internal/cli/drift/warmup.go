package drift

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/kurisu-agent/drift/internal/config"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/warmup"
)

// warmupCmd is `drift warmup` — an interactive wizard for first-time setup.
type warmupCmd struct {
	SkipCircuits   bool `name:"skip-circuits" help:"Skip the circuit phase (assume already configured)."`
	SkipCharacters bool `name:"skip-characters" help:"Skip the character phase."`
	NoProbe        bool `name:"no-probe" help:"Skip the server.version probe (offline setup)."`
}

// runWarmup adapts the CLI flags to the warmup library. stdin TTY-detection
// happens here — the library accepts an already-decided bool so tests can
// exercise both modes without spoofing file descriptors.
func runWarmup(ctx context.Context, io IO, root *CLI, cmd warmupCmd, deps deps) int {
	isTTY := stdinIsTTY(io.Stdin)
	opts := warmup.Options{
		SkipCircuits:   cmd.SkipCircuits,
		SkipCharacters: cmd.SkipCharacters,
		NoProbe:        cmd.NoProbe,
		IsTTY:          isTTY,
	}

	cfgPath, err := deps.clientConfigPath()
	if err != nil {
		return emitError(io, err)
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
		Call: deps.call,
		Now:  time.Now,
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
	return emitError(io, err)
}

// stdinIsTTY reports whether the drift process's stdin is connected to a
// terminal. We avoid pulling golang.org/x/term for one call — checking the
// *os.File mode covers the cases the tests exercise (files, pipes, and real
// TTYs). A non-*os.File reader (as in unit tests that pass a bytes.Buffer)
// is treated as non-TTY; those tests drive warmup.Run directly and pass
// IsTTY explicitly.
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
