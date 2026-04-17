// Package cliscript wires the drift and lakitu CLIs into testscript's
// in-process command map. Tests can then invoke `drift …` and `lakitu …`
// in txtar scripts without building real binaries.
package cliscript

import (
	"context"
	"os"

	driftcli "github.com/kurisu-agent/drift/internal/cli/drift"
	lakitucli "github.com/kurisu-agent/drift/internal/cli/lakitu"
)

// Commands returns the map testscript.Main uses to dispatch the pseudo-
// commands that appear in txtar scripts. Each entry exits the process with
// the corresponding CLI's return code.
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
