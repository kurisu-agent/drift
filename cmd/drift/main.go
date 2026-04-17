// drift — client CLI for remote devcontainer workspaces.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	driftcli "github.com/kurisu-agent/drift/internal/cli/drift"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(driftcli.Run(ctx, os.Args[1:], driftcli.IO{
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Stdin:  os.Stdin,
	}))
}
