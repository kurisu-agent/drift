// lakitu — circuit-side server for drift.
package main

import (
	"context"
	"encoding/json"
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
				buf, _ := e.MarshalJSON()
				var we wire.Error
				_ = json.Unmarshal(buf, &we)
				_ = wire.EncodeResponse(os.Stdout, &wire.Response{
					JSONRPC: wire.Version,
					Error:   &we,
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
