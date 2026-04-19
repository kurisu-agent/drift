package main

import (
	"context"
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
// under Termux, exec wrapping sometimes injects the binary's own path as
// an extra argv[1], so plain `drift` arrives as argv=[name, /path/to/drift]
// and Kong rejects the path as an unexpected positional. We only strip
// when argv[1] exactly matches the resolved executable — a legitimate
// path argument like `drift connect /foo` is untouched.
func cliArgs(argv []string) []string {
	if len(argv) < 2 {
		return nil
	}
	args := argv[1:]
	exe, err := os.Executable()
	if err != nil {
		return args
	}
	if args[0] == exe {
		return args[1:]
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil && args[0] == real {
		return args[1:]
	}
	return args
}
