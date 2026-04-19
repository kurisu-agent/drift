// Package cliscript wires drift/lakitu into testscript's in-process
// command map so txtar scripts invoke them without building binaries.
package cliscript

import (
	"context"
	"os"

	driftcli "github.com/kurisu-agent/drift/internal/cli/drift"
	lakitucli "github.com/kurisu-agent/drift/internal/cli/lakitu"
)

func Commands() map[string]func() {
	return map[string]func(){
		"drift": func() {
			os.Exit(driftcli.Run(context.Background(), os.Args[1:], driftcli.IO{
				Stdout: os.Stdout,
				Stderr: os.Stderr,
				Stdin:  os.Stdin,
			}))
		},
		"lakitu": func() {
			os.Exit(lakitucli.Run(context.Background(), os.Args[1:], lakitucli.IO{
				Stdout: os.Stdout,
				Stderr: os.Stderr,
				Stdin:  os.Stdin,
			}))
		},
	}
}
