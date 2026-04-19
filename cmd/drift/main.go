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

// cliArgs returns argv[1:] with a Termux-specific workaround.
//
// Termux on Android 10+ can't exec files from app-data dirs directly
// (W^X SELinux restrictions), so termux-exec's LD_PRELOAD rewrites
// every execve of such a binary to run through /system/bin/linker64,
// passing the real binary path as an argument. The target binary
// therefore starts with argv[1] set to its own path (pushed in by the
// linker wrapper) ahead of the caller's original arguments — so a
// plain `drift` lands in main as argv=[name, /path/to/drift] and Kong
// rejects the path as an unexpected positional.
//
// Two consequences for detection:
//  1. /proc/self/exe points at /system/bin/linker64, not the drift
//     binary, so os.Executable() returns the wrong thing. termux-exec
//     works around this by exporting the real binary path in the
//     $TERMUX_EXEC__PROC_SELF_EXE env var — we check that first.
//  2. When running outside Termux we still want a defense-in-depth
//     check, so fall back to os.Executable string match, then to an
//     os.SameFile (inode) compare to catch canonicalization drift.
//
// A legitimate path argument like `drift connect /tmp/foo` never
// collides because /tmp/foo has a different inode than the drift
// binary. Set DRIFT_TERMUX_DEBUG=1 to dump argv + resolved paths to
// stderr when diagnosing further exec-wrapper quirks.
func cliArgs(argv []string) []string {
	if os.Getenv("DRIFT_TERMUX_DEBUG") != "" {
		exe, _ := os.Executable()
		fmt.Fprintf(os.Stderr,
			"drift-debug: argv=%q exe=%q TERMUX_EXEC__PROC_SELF_EXE=%q\n",
			argv, exe, os.Getenv("TERMUX_EXEC__PROC_SELF_EXE"))
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
	if termuxExe := os.Getenv("TERMUX_EXEC__PROC_SELF_EXE"); termuxExe != "" && p == termuxExe {
		return true
	}
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
