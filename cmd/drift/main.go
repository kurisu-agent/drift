package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	driftcli "github.com/kurisu-agent/drift/internal/cli/drift"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(driftcli.Run(ctx, cliArgs(os.Args), driftcli.IO{
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Stdin:  os.Stdin,
	}))
}

// cliArgs returns argv[1:] with a Termux-specific workaround: on Android
// under Termux, exec wrapping injects the binary's own path as an extra
// argv[1], so plain `drift` arrives as argv=[name, /path/to/drift] and
// Kong rejects the path as an unexpected positional. We strip argv[1]
// only when it refers to the same underlying file as /proc/self/exe
// (inode+device match via os.SameFile) — a legitimate path argument
// like `drift connect /tmp/foo` never collides because that file isn't
// the drift binary. Using os.SameFile instead of string equality avoids
// canonicalization mismatches between argv[1] and os.Executable() that
// bit the first version of this workaround on Termux.
//
// Set DRIFT_TERMUX_DEBUG=1 to dump argv + executable paths to stderr;
// useful when diagnosing further exec-wrapper quirks.
func cliArgs(argv []string) []string {
	if os.Getenv("DRIFT_TERMUX_DEBUG") != "" {
		exe, _ := os.Executable()
		fmt.Fprintf(os.Stderr, "drift-debug: argv=%q exe=%q\n", argv, exe)
	}
	if len(argv) < 2 {
		return nil
	}
	args := argv[1:]
	if isSelfPath(args[0]) {
		return args[1:]
	}
	return args
}

func isSelfPath(p string) bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	if p == exe {
		return true
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil && p == real {
		return true
	}
	// p comes from argv and exe from os.Executable(); both are stat-only
	// inode comparisons, no read/open, so gosec's taint warning doesn't
	// apply here.
	argInfo, err := os.Stat(p) // #nosec G703 -- stat for SameFile compare, no read/open
	if err != nil {
		return false
	}
	exeInfo, err := os.Stat(exe) // #nosec G703 -- stat for SameFile compare, no read/open
	if err != nil {
		return false
	}
	return os.SameFile(argInfo, exeInfo)
}
