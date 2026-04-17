// Package drift contains the Kong CLI definition for the drift client binary.
//
// This scaffolding intentionally covers only `version` — enough for the
// testscript harness to exercise a real end-to-end invocation. Subcommands
// land as handlers are implemented.
package drift

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/alecthomas/kong"
	"github.com/kurisu-agent/drift/internal/version"
)

// CLI is the root argument parser for drift.
type CLI struct {
	Debug            bool   `help:"Verbose output." env:"DRIFT_DEBUG"`
	SkipVersionCheck bool   `name:"skip-version-check" help:"Bypass drift↔lakitu semver check."`
	Circuit          string `short:"c" help:"Target circuit (overrides default)."`

	Version versionCmd `cmd:"" help:"Print drift version."`
}

type versionCmd struct {
	Output string `enum:"text,json" default:"text" help:"Output format."`
}

// IO bundles the stdio streams so tests can inject buffers.
type IO struct {
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader
}

// Run parses argv and dispatches. It returns a process exit code rather than
// calling os.Exit so tests and in-process harnesses can drive it.
func Run(ctx context.Context, argv []string, io IO) int {
	var cli CLI
	parser, err := kong.New(&cli,
		kong.Name("drift"),
		kong.Description("drift — stateless client for remote devcontainer workspaces."),
		kong.Writers(io.Stdout, io.Stderr),
		kong.Exit(func(int) {}),
	)
	if err != nil {
		fmt.Fprintf(io.Stderr, "drift: %v\n", err)
		return 1
	}
	kctx, err := parser.Parse(argv)
	if err != nil {
		fmt.Fprintf(io.Stderr, "drift: %v\n", err)
		return 2
	}
	switch kctx.Command() {
	case "version":
		return runVersion(io, cli.Version)
	default:
		fmt.Fprintf(io.Stderr, "drift: unknown command %q\n", kctx.Command())
		return 2
	}
}

func runVersion(io IO, cmd versionCmd) int {
	info := version.Get()
	switch cmd.Output {
	case "json":
		buf, err := json.Marshal(info)
		if err != nil {
			fmt.Fprintf(io.Stderr, "drift: %v\n", err)
			return 1
		}
		fmt.Fprintln(io.Stdout, string(buf))
	default:
		fmt.Fprintf(io.Stdout, "drift %s\n", info.Version)
	}
	return 0
}
