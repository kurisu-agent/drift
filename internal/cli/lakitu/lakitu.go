// Package lakitu is the Kong CLI for the lakitu server binary. `rpc`
// dispatches to handlers in a [*rpc.Registry]; other subcommands are
// human-facing counterparts.
package lakitu

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/alecthomas/kong"
	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/config"
	"github.com/kurisu-agent/drift/internal/devpod"
	driftexec "github.com/kurisu-agent/drift/internal/exec"
	"github.com/kurisu-agent/drift/internal/kart"
	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/server"
	"github.com/kurisu-agent/drift/internal/systemd"
	"github.com/kurisu-agent/drift/internal/version"
	"github.com/kurisu-agent/drift/internal/wire"
)

type CLI struct {
	Debug bool `help:"Verbose output." env:"LAKITU_DEBUG"`

	Version   versionCmd   `cmd:"" help:"Print lakitu version."`
	Help      helpCmd      `cmd:"" help:"Print an LLM-friendly command + protocol reference."`
	Init      initCmd      `cmd:"" help:"Bootstrap the garage at ~/.drift/garage (idempotent)."`
	RPC       rpcCmd       `cmd:"" name:"rpc" help:"Read one JSON-RPC 2.0 request from stdin and write a response to stdout."`
	List      kartListCmd  `cmd:"" help:"List karts known to this circuit."`
	Info      kartInfoCmd  `cmd:"" help:"Show one kart's state (JSON)."`
	Kart      kartCmd      `cmd:"" help:"Kart subcommands (new; list/info are top-level)."`
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

type IO struct {
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader
}

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
	case "kart new <name>":
		return runKartNewLocal(ctx, io, cli.Kart.New)
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

// Registry is rebuilt on every Run call so tests can swap handlers.
func Registry() *rpc.Registry {
	reg := rpc.NewRegistry()
	reg.Register(wire.MethodServerInit, serverInitHandler)
	server.RegisterServer(reg, &server.Deps{})
	garage, err := config.GarageDir()
	if err == nil {
		// Verbose mode: tee every devpod subprocess's stdout+stderr to
		// our own stderr so the SSH transport relays it to the drift
		// client live (drift sets LAKITU_DEBUG=1 on the SSH command
		// when its own --debug is on). Argv echoes ride the same path.
		// Wrap in driftexec.RedactingWriter so phase markers and
		// resolver dumps that mention dechested URLs (with embedded
		// PATs) get scrubbed before they reach the operator's terminal.
		// devpod.Client's internal streamMirror wraps again — RedactSecrets
		// is idempotent so the double-pass is harmless.
		var mirror io.Writer
		if os.Getenv("LAKITU_DEBUG") != "" {
			mirror = &driftexec.RedactingWriter{W: os.Stderr}
		}
		// DEVPOD_HOME isolation: every drift-managed devpod invocation
		// lands under ~/.drift/devpod/ so the user's ~/.devpod/ is
		// literally invisible to drift and vice versa. Resolve empty on
		// a hostile environment — drift falls back to the shared HOME
		// with a single-line warning rather than refusing to start.
		driftDevpod, homeErr := config.DriftDevpodHome()
		if homeErr != nil {
			fmt.Fprintf(os.Stderr, "warning: could not resolve DEVPOD_HOME: %v\n", homeErr)
			driftDevpod = ""
		}
		// Pinned devpod: ensure <driftHome>/bin/devpod is the exact
		// release asset this lakitu was built against. Happy path on
		// every run after the first is a cheap hash compare; first run
		// downloads ~117MB. Fall back to $PATH when the fetch fails so
		// a transient network glitch doesn't brick the whole RPC
		// server — the operator sees the warning and can retry later.
		driftHome, _ := config.DriftHomeDir()
		pinnedBin := ""
		if driftHome != "" {
			p, perr := devpod.EnsurePinned(context.Background(), driftHome)
			if perr != nil {
				fmt.Fprintf(os.Stderr, "warning: pinned devpod unavailable (%v); falling back to $PATH\n", perr)
			}
			pinnedBin = p
		}
		lifeDeps := &server.Deps{GarageDir: garage}
		kartDeps := server.KartDeps{
			Devpod:    &devpod.Client{Binary: pinnedBin, Mirror: mirror, DevpodHome: driftDevpod},
			GarageDir: garage,
			OpenChest: lifeDeps.OpenChestForLifecycle,
		}
		server.RegisterKart(reg, kartDeps)
		server.RegisterKartLifecycle(reg, kartDeps)
		server.RegisterKartMigrate(reg, server.KartMigrateDeps{KartDeps: kartDeps})
		server.RegisterKartNew(reg, server.KartNewDeps{
			Deps: &server.Deps{GarageDir: garage},
			Kart: kart.NewDeps{
				GarageDir: garage,
				Devpod:    &devpod.Client{Binary: pinnedBin, Mirror: mirror, DevpodHome: driftDevpod},
			},
			// Same sink as the devpod tee: phase markers, resolver dump,
			// and chest dechest events stream alongside devpod's output
			// over the SSH transport's stderr.
			Verbose: mirror,
		})
		server.RegisterKartAutostart(reg, server.KartAutostartDeps{
			GarageDir: garage,
			Systemd:   &systemd.Client{},
		})
	}
	return reg
}

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
	runsCreated, rerr := config.EnsureRunsYAML(driftHome)
	if rerr != nil {
		return errfmt.Emit(io.Stderr, rerr)
	}
	recipeCreated, rcerr := config.EnsureScaffolderRecipe(driftHome)
	if rcerr != nil {
		return errfmt.Emit(io.Stderr, rcerr)
	}
	if len(res.Created) == 0 && !claudeCreated && !runsCreated && !recipeCreated {
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
		if runsCreated {
			fmt.Fprintf(io.Stdout, "  + %s\n", "../runs.yaml")
		}
		if recipeCreated {
			fmt.Fprintf(io.Stdout, "  + %s\n", "../recipes/scaffolder.md")
		}
	}
	added, perr := ensureDockerProvider(ctx)

	// When devpod is simply absent the provider check and version check
	// both fail with the same root cause — collapse to one actionable
	// warning instead of two copies of os/exec's nested "exec: devpod: …".
	if devpod.IsNotInstalled(perr) {
		fmt.Fprintf(io.Stderr,
			"warning: devpod not installed — circuit won't be usable until it is.\n"+
				"  install: %s\n",
			devpod.InstallHint())
		return 0
	}

	switch {
	case perr != nil:
		// Don't fail init — the garage is already set up and the user can
		// fix devpod without re-running init.
		fmt.Fprintf(io.Stderr, "warning: devpod provider check failed: %v\n", perr)
	case added:
		fmt.Fprintln(io.Stdout, "  + devpod provider: docker")
	}

	// Surface the version check so a mismatched binary is visible
	// immediately, not as mystery errors on kart.new.
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

func serverInitHandler(ctx context.Context, params json.RawMessage) (any, error) {
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
	if created, rerr := config.EnsureRunsYAML(driftHome); rerr != nil {
		return nil, rpcerr.Internal("write runs.yaml: %v", rerr).Wrap(rerr)
	} else if created {
		res.Created = append(res.Created, "../runs.yaml")
	}
	if created, rcerr := config.EnsureScaffolderRecipe(driftHome); rcerr != nil {
		return nil, rpcerr.Internal("write scaffolder recipe: %v", rcerr).Wrap(rcerr)
	} else if created {
		res.Created = append(res.Created, "../recipes/scaffolder.md")
	}
	// Best-effort provider registration. Errors are swallowed — `Created`
	// is for filesystem paths, not diagnostic lines. The drift client sees
	// devpod problems on the first kart.new.
	_, _ = ensureDockerProvider(ctx)
	return res, nil
}

// ensureDockerProvider: first-run `devpod up` fails with "provider with
// name docker not found" unless this has run — folding it into init saves
// users a broken first `drift new`. The provider has to be registered in
// the same DEVPOD_HOME that kart.new will read from later (drift's own
// home, not the user's ~/.devpod/), and it uses the pinned devpod when
// present so the init-time check matches the runtime binary exactly.
func ensureDockerProvider(ctx context.Context) (added bool, err error) {
	home, _ := config.DriftDevpodHome()
	driftHome, _ := config.DriftHomeDir()
	var pinned string
	if driftHome != "" {
		pinned, _ = devpod.EnsurePinned(ctx, driftHome)
	}
	dev := &devpod.Client{Binary: pinned, DevpodHome: home}
	return dev.EnsureProvider(ctx, "docker")
}

// runRPC honors the stdout invariant: only the JSON-RPC response (or
// nothing on a hard crash) ever goes to stdout.
func runRPC(ctx context.Context, io IO, reg *rpc.Registry) int {
	req, err := wire.DecodeRequest(io.Stdin)
	if err != nil {
		// Parse error — no valid id to echo. Emit a response with null id
		// per JSON-RPC 2.0 so drift can still branch on the envelope.
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
