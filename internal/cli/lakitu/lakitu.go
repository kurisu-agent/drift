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
	debug := cli.Debug || os.Getenv("LAKITU_DEBUG") != ""
	switch kctx.Command() {
	case "version":
		return runVersion(io, cli.Version)
	case "help":
		return runHelp(io, parser)
	case "init":
		return runInit(ctx, io)
	case "rpc":
		return runRPC(ctx, io, debug)
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
	case "character new <name>":
		return runCharacterNew(ctx, io, cli.Character.New)
	case "character set <name> <field> <value>":
		return runCharacterSet(ctx, io, cli.Character.Set)
	case "character unset <name> <field>":
		return runCharacterUnset(ctx, io, cli.Character.Unset)
	case "character edit <name>":
		return runCharacterEdit(ctx, io, cli.Character.Edit)
	case "character list":
		return runCharacterList(ctx, io)
	case "character show <name>":
		return runCharacterShow(ctx, io, cli.Character.Show)
	case "character rm <name>":
		return runCharacterRemove(ctx, io, cli.Character.Remove)
	case "tune new <name>":
		return runTuneNew(ctx, io, cli.Tune.New)
	case "tune set <name> <field> <value>":
		return runTuneSet(ctx, io, cli.Tune.Set)
	case "tune unset <name> <field>":
		return runTuneUnset(ctx, io, cli.Tune.Unset)
	case "tune edit <name>":
		return runTuneEdit(ctx, io, cli.Tune.Edit)
	case "tune list":
		return runTuneList(ctx, io)
	case "tune show <name>":
		return runTuneShow(ctx, io, cli.Tune.Show)
	case "tune rm <name>":
		return runTuneRemove(ctx, io, cli.Tune.Remove)
	case "chest new <name>":
		return runChestNew(ctx, io, cli.Chest.New)
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
		fmt.Fprintln(io.Stdout, info.Format("lakitu"))
	}
	return 0
}

// Registry builds the full handler registry — including the devpod-backed
// kart handlers — and is rebuilt on every call so tests can swap handlers.
// Dispatch paths that know the method up front (runRPC, callAndPrint)
// should prefer buildRegistry(ctx, methodNeedsDevpod(m), debug) directly,
// so non-devpod methods skip resolvePinnedDevpod / EnsurePinned entirely.
// Registry() stays as the "give me everything" convenience for callers
// that don't have a method in hand.
func Registry() *rpc.Registry {
	reg, err := buildRegistry(context.Background(), true, os.Getenv("LAKITU_DEBUG") != "")
	if err != nil {
		// GarageDir() only fails on a hostile $HOME. Surface it loudly
		// rather than silently skipping kart handlers — a missing garage
		// is a config problem the operator needs to see, not a stealth
		// "method not found".
		fmt.Fprintf(os.Stderr, "lakitu: %v\n", err)
	}
	return reg
}

// buildRegistry assembles a registry for dispatch. When withDevpod is
// false the kart-lifecycle handlers are omitted entirely — no
// resolvePinnedDevpod / EnsurePinned call runs, so non-devpod RPCs
// (server.*, config.*, character.*, chest.*, tune.*) stay fast and
// offline-safe. An error is returned when GarageDir() fails AND the
// caller asked for devpod handlers; non-devpod registration tolerates
// a missing garage because server.version has to work on a fresh box.
func buildRegistry(ctx context.Context, withDevpod, debug bool) (*rpc.Registry, error) {
	reg := rpc.NewRegistry()
	registerNonDevpod(reg)
	if !withDevpod {
		return reg, nil
	}
	garage, err := config.GarageDir()
	if err != nil {
		return reg, fmt.Errorf("resolve garage dir: %w", err)
	}
	registerDevpodBacked(ctx, reg, garage, debug)
	return reg, nil
}

// registerNonDevpod wires handlers that only touch the garage tree (or
// don't touch anything at all). Zero subprocess cost — safe to run on
// every lakitu invocation even when the caller will only dispatch one
// method. server.init gets a custom handler here because it needs a
// DriftHomeDir lookup that isn't part of the generic server.Deps bundle.
func registerNonDevpod(reg *rpc.Registry) {
	reg.Register(wire.MethodServerInit, serverInitHandler)
	server.RegisterServer(reg, &server.Deps{})
}

// registerDevpodBacked wires the kart-lifecycle handlers. Materializes
// the pinned devpod binary once and reuses a single devpod.Client /
// server.Deps across every registration — the previous code constructed
// each twice.
func registerDevpodBacked(ctx context.Context, reg *rpc.Registry, garage string, debug bool) {
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
	if debug {
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
	//
	// DRIFT_DEVPOD_BINARY short-circuits the download entirely —
	// useful for operators pointing at a custom build and for the
	// integration harness, which installs a devpod shim at a fixed
	// path that would otherwise be shadowed by the pinned binary.
	pinnedBin := resolvePinnedDevpod(ctx)
	lifeDeps := &server.Deps{GarageDir: garage}
	dev := &devpod.Client{Binary: pinnedBin, Mirror: mirror, DevpodHome: driftDevpod}
	kartDeps := server.KartDeps{
		Devpod:    dev,
		GarageDir: garage,
		OpenChest: lifeDeps.OpenChestForLifecycle,
	}
	server.RegisterKart(reg, kartDeps)
	server.RegisterKartLifecycle(reg, kartDeps)
	server.RegisterKartConnect(reg, kartDeps)
	server.RegisterKartMigrate(reg, server.KartMigrateDeps{KartDeps: kartDeps})
	server.RegisterKartNew(reg, server.KartNewDeps{
		Deps: lifeDeps,
		Kart: kart.NewDeps{
			GarageDir: garage,
			Devpod:    dev,
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

// methodNeedsDevpod reports whether dispatching the named method will
// reach a handler that spawns devpod subprocesses. The RPC fast path
// uses this to skip resolvePinnedDevpod/EnsurePinned for non-kart
// methods. Keep in sync with registerDevpodBacked.
func methodNeedsDevpod(method string) bool {
	switch method {
	case wire.MethodKartNew, wire.MethodKartStart, wire.MethodKartStop,
		wire.MethodKartRestart, wire.MethodKartRecreate,
		wire.MethodKartRebuild, wire.MethodKartDriftCheck,
		wire.MethodKartDelete, wire.MethodKartList,
		wire.MethodKartInfo, wire.MethodKartEnable, wire.MethodKartDisable,
		wire.MethodKartLogs, wire.MethodKartSessionEnv,
		wire.MethodKartMigrateList, wire.MethodKartConnect:
		return true
	}
	return false
}

func runInit(ctx context.Context, io IO) int {
	root, err := config.GarageDir()
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	driftHome, herr := config.DriftHomeDir()
	if herr != nil {
		return errfmt.Emit(io.Stderr, herr)
	}
	res, err := config.InitGarageFull(root, driftHome)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	if len(res.Created) == 0 {
		fmt.Fprintf(io.Stdout, "garage already initialized at %s\n", res.GarageDir)
	} else {
		created := append([]string(nil), res.Created...)
		sort.Strings(created)
		fmt.Fprintf(io.Stdout, "initialized garage at %s\n", res.GarageDir)
		for _, c := range created {
			fmt.Fprintf(io.Stdout, "  + %s\n", c)
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
	driftHome, herr := config.DriftHomeDir()
	if herr != nil {
		return nil, rpcerr.Internal("resolve drift home: %v", herr).Wrap(herr)
	}
	res, err := config.InitGarageFull(root, driftHome)
	if err != nil {
		return nil, rpcerr.Internal("init garage: %v", err).Wrap(err)
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
	dev := &devpod.Client{Binary: resolvePinnedDevpod(ctx), DevpodHome: home}
	return dev.EnsureProvider(ctx, "docker")
}

// resolvePinnedDevpod returns the path to the devpod binary lakitu should
// spawn for kart operations. DRIFT_DEVPOD_BINARY overrides everything (used
// by operators testing a custom build and by the integration harness whose
// shim lives at a fixed path). Otherwise we materialize the pinned release
// under <driftHome>/bin/devpod via EnsurePinned and fall back to $PATH on
// any failure — the warning tells the operator what happened.
func resolvePinnedDevpod(ctx context.Context) string {
	if override := os.Getenv("DRIFT_DEVPOD_BINARY"); override != "" {
		return override
	}
	driftHome, _ := config.DriftHomeDir()
	if driftHome == "" {
		return ""
	}
	p, err := devpod.EnsurePinned(ctx, driftHome)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: pinned devpod unavailable (%v); falling back to $PATH\n", err)
	}
	return p
}

// runRPC honors the stdout invariant: only the JSON-RPC response (or
// nothing on a hard crash) ever goes to stdout. The registry is built
// per-request around the decoded method so non-devpod RPCs don't pay
// resolvePinnedDevpod / EnsurePinned cost.
func runRPC(ctx context.Context, io IO, debug bool) int {
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
	needDevpod := methodNeedsDevpod(req.Method)
	reg, regErr := buildRegistry(ctx, needDevpod, debug)
	if regErr != nil && needDevpod {
		// Devpod-backed method on a circuit whose garage can't be
		// resolved — surface as an RPC error instead of a silent
		// "method not found". Non-devpod callers still get the
		// (partial) registry so server.version on a hostile $HOME
		// keeps working.
		e := rpcerr.Internal("registry: %v", regErr).Wrap(regErr)
		resp := &wire.Response{
			JSONRPC: wire.Version,
			ID:      req.ID,
			Error:   e.Wire(),
		}
		_ = wire.EncodeResponse(io.Stdout, resp)
		return 0
	}
	resp := reg.Dispatch(ctx, req)
	_ = wire.EncodeResponse(io.Stdout, resp)
	return 0
}
