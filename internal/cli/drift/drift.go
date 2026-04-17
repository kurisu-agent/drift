// Package drift contains the Kong CLI definition for the drift client binary.
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
	Output           string `name:"output" help:"Output format for structured commands." enum:"text,json" default:"text"`

	Version versionCmd `cmd:"" help:"Print drift version."`
	Circuit_ circuitCmd `cmd:"" name:"circuit" help:"Manage circuits (client-side config + SSH config)."`
}

type versionCmd struct{}

// IO bundles the stdio streams so tests can inject buffers.
type IO struct {
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader
}

// Run parses argv and dispatches. It returns a process exit code rather than
// calling os.Exit so tests and in-process harnesses can drive it.
func Run(ctx context.Context, argv []string, io IO) int {
	return run(ctx, argv, io, defaultDeps())
}

// run is the testable entry point — deps is threaded in so tests can stub the
// RPC client and filesystem paths.
func run(ctx context.Context, argv []string, io IO, deps deps) int {
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
		return runVersion(io, cli.Output)
	case "circuit add <name>":
		return runCircuitAdd(ctx, io, &cli, cli.Circuit_.Add, deps)
	case "circuit rm <name>":
		return runCircuitRm(io, &cli, cli.Circuit_.Rm, deps)
	case "circuit list":
		return runCircuitList(io, &cli, deps)
	default:
		fmt.Fprintf(io.Stderr, "drift: unknown command %q\n", kctx.Command())
		return 2
	}
}

func runVersion(io IO, outputFormat string) int {
	info := version.Get()
	switch outputFormat {
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
