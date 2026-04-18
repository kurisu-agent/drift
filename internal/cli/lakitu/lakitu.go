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
	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/config"
	"github.com/kurisu-agent/drift/internal/devpod"
	"github.com/kurisu-agent/drift/internal/kart"
	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/server"
	"github.com/kurisu-agent/drift/internal/systemd"
	"github.com/kurisu-agent/drift/internal/version"
	"github.com/kurisu-agent/drift/internal/wire"
)

// CLI is the root argument parser for lakitu.
type CLI struct {
	Debug bool `help:"Verbose output." env:"LAKITU_DEBUG"`

	Version   versionCmd   `cmd:"" help:"Print lakitu version."`
	Help      helpCmd      `cmd:"" help:"Print an LLM-friendly command + protocol reference."`
	Init      initCmd      `cmd:"" help:"Bootstrap the garage at ~/.drift/garage (idempotent)."`
	RPC       rpcCmd       `cmd:"" name:"rpc" help:"Read one JSON-RPC 2.0 request from stdin and write a response to stdout."`
	List      kartListCmd  `cmd:"" help:"List karts known to this circuit."`
	Info      kartInfoCmd  `cmd:"" help:"Show one kart's state (JSON)."`
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
	case "help":
		return runHelp(io, parser)
	case "init":
		return runInit(ctx, io)
	case "rpc":
		return runRPC(ctx, io, Registry())
	case "list":
		return runKartList(ctx, io)
	case "info <name>":
		return runKartInfo(ctx, io, cli.Info)
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
		return errfmt.Emit(io.Stderr, fmt.Errorf("unknown command %q", kctx.Command()))
	}
}

func runVersion(io IO, cmd versionCmd) int {
	info := version.Get()
	switch cmd.Output {
	case "json":
		buf, err := json.Marshal(info)
		if err != nil {
			return errfmt.Emit(io.Stderr, err)
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
		kartDeps := server.KartDeps{
			Devpod:    &devpod.Client{},
			GarageDir: garage,
		}
		server.RegisterKart(reg, kartDeps)
		server.RegisterKartLifecycle(reg, kartDeps)
		server.RegisterKartNew(reg, server.KartNewDeps{
			Deps: &server.Deps{GarageDir: garage},
			Kart: kart.NewDeps{
				GarageDir: garage,
				Devpod:    &devpod.Client{},
			},
		})
		server.RegisterKartAutostart(reg, server.KartAutostartDeps{
			GarageDir: garage,
			Systemd:   &systemd.Client{},
		})
	}
	return reg
}

// runInit is the human-CLI counterpart of the server.init RPC. It bootstraps
// the garage, ensures the devpod docker provider is registered, and prints
// a short, stable summary to stdout. Errors flow through errfmt.Emit for
// the two-line format.
func runInit(ctx context.Context, io IO) int {
	root, err := config.GarageDir()
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	res, err := config.InitGarage(root)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	driftHome, herr := config.DriftHomeDir()
	if herr != nil {
		return errfmt.Emit(io.Stderr, herr)
	}
	claudeCreated, cerr := config.EnsureClaudeMD(driftHome)
	if cerr != nil {
		return errfmt.Emit(io.Stderr, cerr)
	}
	if len(res.Created) == 0 && !claudeCreated {
		fmt.Fprintf(io.Stdout, "garage already initialized at %s\n", res.GarageDir)
	} else {
		created := append([]string(nil), res.Created...)
		sort.Strings(created)
		fmt.Fprintf(io.Stdout, "initialized garage at %s\n", res.GarageDir)
		for _, c := range created {
			fmt.Fprintf(io.Stdout, "  + %s\n", c)
		}
		if claudeCreated {
			fmt.Fprintf(io.Stdout, "  + %s\n", "../CLAUDE.md")
		}
	}
	added, perr := ensureDockerProvider(ctx)
	switch {
	case perr != nil:
		// Don't fail init on this — a circuit with a missing devpod binary
		// is a real problem, but the garage is already set up and the user
		// can fix devpod without re-running init.
		fmt.Fprintf(io.Stderr, "warning: devpod provider check failed: %v\n", perr)
	case added:
		fmt.Fprintln(io.Stdout, "  + devpod provider: docker")
	}

	// Surface the devpod version check so a mismatched binary is visible
	// immediately rather than showing up as mystery errors on kart.new.
	dev := &devpod.Client{}
	if vc, verr := dev.Verify(ctx); verr != nil {
		fmt.Fprintf(io.Stderr, "warning: could not determine devpod version: %v\n", verr)
	} else if vc.Expected == "" {
		fmt.Fprintf(io.Stdout, "  devpod: %s (no pin — dev build)\n", vc.Actual)
	} else if vc.Match {
		fmt.Fprintf(io.Stdout, "  devpod: %s (matches pin)\n", vc.Actual)
	} else {
		fmt.Fprintf(io.Stderr,
			"warning: devpod version mismatch: have %s, lakitu was built against %s\n",
			vc.Actual, vc.Expected)
	}
	return 0
}

// serverInitHandler is the RPC-facing counterpart of `lakitu init`. It
// resolves the garage root from the server's environment ($HOME) and
// returns the same InitResult both paths share.
func serverInitHandler(ctx context.Context, params json.RawMessage) (any, error) {
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
	driftHome, herr := config.DriftHomeDir()
	if herr != nil {
		return nil, rpcerr.Internal("resolve drift home: %v", herr).Wrap(herr)
	}
	if created, cerr := config.EnsureClaudeMD(driftHome); cerr != nil {
		return nil, rpcerr.Internal("write CLAUDE.md: %v", cerr).Wrap(cerr)
	} else if created {
		res.Created = append(res.Created, "../CLAUDE.md")
	}
	if _, perr := ensureDockerProvider(ctx); perr != nil {
		// Same rationale as runInit: the garage exists; surface the devpod
		// hiccup but don't let it mask a successful init.
		res.Created = append(res.Created, fmt.Sprintf("warning: devpod provider check failed: %v", perr))
	}
	return res, nil
}

// ensureDockerProvider idempotently registers the docker provider with
// devpod. First-run `devpod up` fails with "provider with name docker not
// found" unless this has run — folding it into init saves users the
// surprise of a broken first `drift new`.
func ensureDockerProvider(ctx context.Context) (added bool, err error) {
	dev := &devpod.Client{}
	return dev.EnsureProvider(ctx, "docker")
}

// runRPC is the one-shot dispatch entry point. It honors the stdout
// invariant: only the JSON-RPC response (or nothing on a hard crash) ever
// goes to stdout.
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
