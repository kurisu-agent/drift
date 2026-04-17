// Package lakitu contains the Kong CLI definition for the lakitu server binary.
//
// Scaffolding covers `version` and `rpc`. The `rpc` command dispatches to
// handlers registered in a [*rpc.Registry]; wiring for specific methods lands
// in later phases.
package lakitu

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"github.com/alecthomas/kong"
	"github.com/kurisu-agent/drift/internal/config"
	"github.com/kurisu-agent/drift/internal/devpod"
	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/server"
	"github.com/kurisu-agent/drift/internal/version"
	"github.com/kurisu-agent/drift/internal/wire"
)

// CLI is the root argument parser for lakitu.
type CLI struct {
	Debug bool `help:"Verbose output." env:"LAKITU_DEBUG"`

	Version   versionCmd   `cmd:"" help:"Print lakitu version."`
	Init      initCmd      `cmd:"" help:"Bootstrap the garage at ~/.drift/garage (idempotent)."`
	RPC       rpcCmd       `cmd:"" name:"rpc" help:"Read one JSON-RPC 2.0 request from stdin and write a response to stdout."`
	Config    configCmd    `cmd:"" help:"Manage server-level config."`
	Character characterCmd `cmd:"" help:"Manage character (git/GitHub identity) profiles."`
	Tune      tuneCmd      `cmd:"" help:"Manage tune profiles."`
	Chest     chestCmd     `cmd:"" help:"Manage secrets in the chest backend."`
}

type versionCmd struct {
	Output string `enum:"text,json" default:"text" help:"Output format."`
}

type initCmd struct{}

type rpcCmd struct{}

// IO bundles the stdio streams.
type IO struct {
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader
}

// Run parses argv and dispatches. Returns the process exit code.
func Run(ctx context.Context, argv []string, io IO) int {
	var cli CLI
	parser, err := kong.New(&cli,
		kong.Name("lakitu"),
		kong.Description("lakitu — circuit-side server for drift."),
		kong.Writers(io.Stdout, io.Stderr),
		kong.Exit(func(int) {}),
	)
	if err != nil {
		fmt.Fprintf(io.Stderr, "lakitu: %v\n", err)
		return 1
	}
	kctx, err := parser.Parse(argv)
	if err != nil {
		fmt.Fprintf(io.Stderr, "lakitu: %v\n", err)
		return 2
	}
	switch kctx.Command() {
	case "version":
		return runVersion(io, cli.Version)
	case "init":
		return runInit(io)
	case "rpc":
		return runRPC(ctx, io, Registry())
	case "config show":
		return runConfigShow(ctx, io)
	case "config set <key> <value>":
		return runConfigSet(ctx, io, cli.Config.Set)
	case "character add <name>":
		return runCharacterAdd(ctx, io, cli.Character.Add)
	case "character list":
		return runCharacterList(ctx, io)
	case "character show <name>":
		return runCharacterShow(ctx, io, cli.Character.Show)
	case "character rm <name>":
		return runCharacterRemove(ctx, io, cli.Character.Remove)
	case "tune list":
		return runTuneList(ctx, io)
	case "tune show <name>":
		return runTuneShow(ctx, io, cli.Tune.Show)
	case "tune set <name>":
		return runTuneSet(ctx, io, cli.Tune.Set)
	case "tune rm <name>":
		return runTuneRemove(ctx, io, cli.Tune.Remove)
	case "chest set <name>":
		return runChestSet(ctx, io, cli.Chest.Set)
	case "chest get <name>":
		return runChestGet(ctx, io, cli.Chest.Get)
	case "chest list":
		return runChestList(ctx, io)
	case "chest rm <name>":
		return runChestRemove(ctx, io, cli.Chest.Remove)
	default:
		fmt.Fprintf(io.Stderr, "lakitu: unknown command %q\n", kctx.Command())
		return 2
	}
}

func runVersion(io IO, cmd versionCmd) int {
	info := version.Get()
	switch cmd.Output {
	case "json":
		buf, err := json.Marshal(info)
		if err != nil {
			fmt.Fprintf(io.Stderr, "lakitu: %v\n", err)
			return 1
		}
		fmt.Fprintln(io.Stdout, string(buf))
	default:
		fmt.Fprintf(io.Stdout, "lakitu %s\n", info.Version)
	}
	return 0
}

// Registry returns the method registry used by `lakitu rpc`. Handlers are
// registered here as they come online in later phases. The registry is
// rebuilt on every Run call so tests can swap it out.
func Registry() *rpc.Registry {
	reg := rpc.NewRegistry()
	reg.Register(wire.MethodServerInit, serverInitHandler)
	server.RegisterServer(reg, &server.Deps{})
	garage, err := config.GarageDir()
	if err == nil {
		server.RegisterKart(reg, server.KartDeps{
			Devpod:    &devpod.Client{},
			GarageDir: garage,
		})
	}
	return reg
}

// runInit is the human-CLI counterpart of the server.init RPC. It bootstraps
// the garage and prints a short, stable summary to stdout. Errors land on
// stderr with a nonzero exit — lakitu's top-level CLI doesn't yet emit the
// structured stderr format (Phase 14), so we keep the message simple.
func runInit(io IO) int {
	root, err := config.GarageDir()
	if err != nil {
		fmt.Fprintf(io.Stderr, "lakitu: %v\n", err)
		return 1
	}
	res, err := config.InitGarage(root)
	if err != nil {
		fmt.Fprintf(io.Stderr, "lakitu: %v\n", err)
		return 1
	}
	if len(res.Created) == 0 {
		fmt.Fprintf(io.Stdout, "garage already initialized at %s\n", res.GarageDir)
		return 0
	}
	created := append([]string(nil), res.Created...)
	sort.Strings(created)
	fmt.Fprintf(io.Stdout, "initialized garage at %s\n", res.GarageDir)
	for _, c := range created {
		fmt.Fprintf(io.Stdout, "  + %s\n", c)
	}
	return 0
}

// serverInitHandler is the RPC-facing counterpart of `lakitu init`. It
// resolves the garage root from the server's environment ($HOME) and
// returns the same InitResult both paths share.
func serverInitHandler(_ context.Context, params json.RawMessage) (any, error) {
	// server.init takes no params. Strict binding rejects stray fields
	// instead of silently ignoring them.
	var p struct{}
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	root, err := config.GarageDir()
	if err != nil {
		return nil, rpcerr.Internal("resolve garage dir: %v", err).Wrap(err)
	}
	res, err := config.InitGarage(root)
	if err != nil {
		return nil, rpcerr.Internal("init garage: %v", err).Wrap(err)
	}
	return res, nil
}

// runRPC is the one-shot dispatch entry point. It honors plans/PLAN.md's
// stdout invariant: only the JSON-RPC response (or nothing on a hard crash)
// ever goes to stdout.
func runRPC(ctx context.Context, io IO, reg *rpc.Registry) int {
	req, err := wire.DecodeRequest(io.Stdin)
	if err != nil {
		// Parse error — no valid id to echo. Emit a response with a null id
		// per JSON-RPC 2.0 so drift can still branch on the envelope shape.
		e := rpcerr.New(rpcerr.CodeInternal, "parse_error", "parse error: %v", err)
		resp := &wire.Response{
			JSONRPC: wire.Version,
			ID:      json.RawMessage("null"),
			Error:   e.Wire(),
		}
		_ = wire.EncodeResponse(io.Stdout, resp)
		return 0
	}
	resp := reg.Dispatch(ctx, req)
	_ = wire.EncodeResponse(io.Stdout, resp)
	return 0
}
