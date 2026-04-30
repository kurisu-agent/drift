package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	lakitucli "github.com/kurisu-agent/drift/internal/cli/lakitu"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/wire"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// A mid-handler panic during `lakitu rpc` must not leave stdout
	// half-written — every client call assumes exactly one JSON object.
	defer func() {
		if r := recover(); r != nil {
			e := rpcerr.Internal("panic: %v", r)
			if len(os.Args) > 1 && os.Args[1] == "rpc" {
				_ = wire.EncodeResponse(os.Stdout, &wire.Response{
					JSONRPC: wire.Version,
					ID:      nil,
					Error:   e.Wire(),
				})
			} else {
				fmt.Fprintf(os.Stderr, "lakitu: %v\n", e)
			}
			os.Exit(1)
		}
	}()

	os.Exit(lakitucli.Run(ctx, os.Args[1:], lakitucli.IO{
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Stdin:  os.Stdin,
	}))
}
