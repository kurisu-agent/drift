// Package drift is the Kong CLI for the drift client binary.
package drift

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/version"
)

type CLI struct {
	Debug            bool   `help:"Verbose output (default on; --no-debug to silence)." env:"DRIFT_DEBUG" default:"true" negatable:""`
	SkipVersionCheck bool   `name:"skip-version-check" hidden:"" env:"DRIFT_SKIP_VERSION_CHECK" help:"Bypass drift↔lakitu semver check."`
	NoColor          bool   `name:"no-color" env:"NO_COLOR" help:"Disable ANSI colors in text output."`
	Circuit          string `short:"c" help:"Target circuit (overrides default)."`
	Output           string `name:"output" short:"o" help:"Output format for structured commands." enum:"text,json" default:"text"`

	// Version is scanned out of argv before Kong parses, so this field is
	// never read — it exists purely to register `-v` / `--version` in the
	// help output Kong auto-generates. See maybeVersionExit.
	Version bool `short:"v" help:"Print drift version and exit."`

	Help   helpCmd   `cmd:"" help:"Print an LLM-friendly command + protocol reference."`
	Init   initCmd   `cmd:"" name:"init" help:"Interactive first-time setup wizard (circuits + characters)."`
	Status statusCmd `cmd:"" name:"status" help:"Show configured circuits + their lakitu health and per-circuit karts."`
	Update updateCmd `cmd:"" name:"update" help:"Check GitHub for a newer drift release and self-install."`

	// Plural list verbs — print-only tables. Singular namespace verbs drop
	// into pickers; plurals are for scripting and at-a-glance inspection.
	Circuits circuitsCmd `cmd:"" name:"circuits" help:"List configured circuits (table)."`
	Karts    kartsCmd    `cmd:"" name:"karts" help:"List karts (cross-circuit by default; scope with -c)."`
	Runs     runsCmd     `cmd:"" name:"runs" help:"List runs.yaml entries on the target circuit."`
	Skills   skillsCmd   `cmd:"" name:"skills" help:"List Claude skills on the target circuit."`

	// Noun namespaces. Bare `drift circuit` / `drift kart` resolve to the
	// default subcommand (connect), so the singular verb acts as a picker.
	Circuit_ circuitCmd `cmd:"" name:"circuit" help:"Circuit-scoped commands (bare: pick + shell)."`
	Kart     kartCmd    `cmd:"" name:"kart" help:"Kart-scoped commands (bare: pick + connect)."`

	// Merged picker — bare `drift connect` fans out over circuits + karts.
	Connect connectCmd `cmd:"" aliases:"into,attach" help:"Pick a circuit or kart and connect (merged picker)."`

	// Top-level lifecycle aliases for the highest-traffic kart verbs.
	// Each forwards to the same runner as `drift kart <verb>`, including
	// the no-arg cross-circuit picker; the namespace form stays for
	// completeness and tab-completion discoverability.
	Start  startCmd  `cmd:"" name:"start" help:"Start a kart (picker when no name)."`
	Stop   stopCmd   `cmd:"" name:"stop" help:"Stop a kart (picker when no name)."`
	Delete deleteCmd `cmd:"" name:"delete" help:"Delete a kart (picker when no name)."`

	// Kart creation stays flat: `drift new` is a frequent top-level verb.
	New newCmd `cmd:"" name:"new" help:"Create a new kart (from starter or existing repo)."`

	// Name-first shortcuts with a single implicit verb (execute / invoke).
	Run   runCmd   `cmd:"" name:"run" help:"Execute a user-script shorthand from runs.yaml."`
	AI    aiCmd    `cmd:"" name:"ai" help:"Launch Claude Code on the circuit (interactive REPL)."`
	Skill skillCmd `cmd:"" name:"skill" help:"Pick / invoke a Claude skill on the circuit."`

	Migrate migrateCmd `cmd:"" name:"migrate" help:"Adopt an existing devpod workspace as a drift kart (interactive)."`

	Ports portsCmd `cmd:"" name:"ports" help:"Workstation-side TCP port forward management."`

	SshProxy sshProxyCmd `cmd:"" name:"ssh-proxy" hidden:"" help:"ProxyCommand helper for drift.<circuit>.<kart> aliases (invoked by OpenSSH)."`
}

type IO struct {
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader
}

func Run(ctx context.Context, argv []string, io IO) int {
	// Termux/Android ships without /etc/resolv.conf, so Go's pure-Go
	// resolver falls back to [::1]:53 and every outbound HTTP call
	// dies with "connection refused". Install a process-wide DNS
	// fallback before any subcommand runs so update / connect / any
	// future net call sees a working resolver.
	installDNSFallback()
	return run(ctx, argv, io, defaultDeps())
}

func run(ctx context.Context, argv []string, io IO, deps deps) int {
	// Intercept -v / --version before Kong's command-required parser rejects
	// a flag-only invocation. Output format is scraped from the same argv so
	// `drift -v --output json` still produces machine output.
	if rc, handled := maybeVersionExit(argv, io); handled {
		return rc
	}

	// No-arg invocation on a real terminal drops into an interactive menu
	// (see menu.go). Non-TTY callers fall through to Kong so scripts and
	// agents continue to see the existing "expected command" error.
	if len(argv) == 0 && stdinIsTTY(io.Stdin) && stdoutIsTTY(io.Stdout) {
		chosen, err := runMenu(io)
		if err != nil {
			return errfmt.Emit(io.Stderr, err)
		}
		if len(chosen) == 0 {
			return 0
		}
		argv = chosen
	}

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

	// --no-color / NO_COLOR disable colors globally by forcing the env var
	// our style package already respects. Done once at dispatch so every
	// subsequent style.For call sees the override.
	if cli.NoColor {
		_ = os.Setenv("NO_COLOR", "1")
	}

	// --debug populates cli.Debug via Kong's env binding, but downstream
	// consumers (the SSH RPC transport that forwards LAKITU_DEBUG=1 to
	// the circuit, any future env-driven verbose toggle) read the env
	// var directly so they don't need a reference to the CLI struct.
	// Re-export so flag-only invocations (`drift --debug new …`) work
	// the same as env-only (`DRIFT_DEBUG=1 drift new …`).
	if cli.Debug {
		_ = os.Setenv("DRIFT_DEBUG", "1")
	}

	// Pre-dispatch hooks: advisory banners (update available, future
	// deprecation notices) + fire-and-forget background checks. Must
	// stay non-blocking — anything network-bound hands off to a
	// goroutine and writes state.json for the next invocation to read.
	runPreDispatch(io, &cli, kctx.Command(), deps)

	switch kctx.Command() {
	case "help":
		return runHelp(io, parser, cli.Help)

	// Plural listings
	case "circuits":
		return runCircuits(io, &cli, deps)
	case "karts":
		return runKarts(ctx, io, &cli, deps)
	case "runs":
		return runRuns(ctx, io, &cli, deps)
	case "skills":
		return runSkills(ctx, io, &cli, deps)

	// Circuit namespace
	case "circuit add", "circuit add <user@host>":
		return runCircuitAdd(ctx, io, &cli, cli.Circuit_.Add, deps)
	case "circuit rm <name>":
		return runCircuitRm(io, &cli, cli.Circuit_.Rm, deps)
	case "circuit set name <new-name>":
		return runCircuitSetName(ctx, io, &cli, cli.Circuit_.Set.Name, deps)
	case "circuit set default", "circuit set default <name>":
		return runCircuitSetDefault(io, &cli, cli.Circuit_.Set.Default, deps)
	case "circuit", "circuit connect", "circuit connect <name>":
		return runCircuitConnect(ctx, io, &cli, cli.Circuit_.Connect, deps)

	// Kart namespace
	case "kart", "kart connect", "kart connect <name>":
		return runKartConnect(ctx, io, &cli, cli.Kart.Connect, deps)
	case "kart info <name>":
		return runKartInfo(ctx, io, &cli, cli.Kart.Info, deps)
	case "kart start", "kart start <name>":
		return runKartStart(ctx, io, &cli, cli.Kart.Start, deps)
	case "kart stop", "kart stop <name>":
		return runKartStop(ctx, io, &cli, cli.Kart.Stop, deps)
	case "kart restart <name>":
		return runKartRestart(ctx, io, &cli, cli.Kart.Restart, deps)
	case "kart recreate <name>":
		return runKartRecreate(ctx, io, &cli, cli.Kart.Recreate, deps)
	case "kart rebuild <name>":
		return runKartRebuild(ctx, io, &cli, cli.Kart.Rebuild, deps)
	case "kart delete", "kart delete <name>":
		return runKartDelete(ctx, io, &cli, cli.Kart.Delete, deps)
	case "kart logs <name>":
		return runKartLogs(ctx, io, &cli, cli.Kart.Logs, deps)
	case "kart enable <name>":
		return runKartEnable(ctx, io, &cli, cli.Kart.Enable, deps)
	case "kart disable <name>":
		return runKartDisable(ctx, io, &cli, cli.Kart.Disable, deps)

	// Top-level verbs
	case "new <name>":
		return runNew(ctx, io, &cli, cli.New, deps)
	case "init":
		return runInit(ctx, io, &cli, cli.Init, deps)
	case "status":
		return runStatus(ctx, io, &cli, cli.Status, deps)
	case "connect", "connect <name>":
		return runConnect(ctx, io, &cli, cli.Connect, deps)
	case "start", "start <name>":
		return runKartStart(ctx, io, &cli, cli.Start, deps)
	case "stop", "stop <name>":
		return runKartStop(ctx, io, &cli, cli.Stop, deps)
	case "delete", "delete <name>":
		return runKartDelete(ctx, io, &cli, cli.Delete, deps)
	case "run", "run <name>", "run <name> <args>":
		return runRunExec(ctx, io, &cli, cli.Run, deps)
	case "ai":
		return runAIExec(ctx, io, &cli, cli.AI, deps)
	case "skill", "skill <name>", "skill <name> <prompt>":
		return runSkillExec(ctx, io, &cli, cli.Skill, deps)
	case "migrate":
		return runMigrate(ctx, io, &cli, cli.Migrate, deps)
	case "ports", "ports list":
		return runPortsList(ctx, io, &cli, cli.Ports.List, deps)
	case "ports add <port>":
		return runPortsAdd(ctx, io, &cli, cli.Ports.Add, deps)
	case "ports rm <port>":
		return runPortsRm(ctx, io, &cli, cli.Ports.Rm, deps)
	case "ports remap <spec>":
		return runPortsRemap(ctx, io, &cli, cli.Ports.Remap, deps)
	case "ports probe":
		return runPortsProbe(ctx, io, &cli, cli.Ports.Probe, deps)
	case "ports up":
		return runPortsUp(ctx, io, &cli, cli.Ports.Up, deps)
	case "ports down":
		return runPortsDown(ctx, io, &cli, cli.Ports.Down, deps)
	case "ports status":
		return runPortsStatus(ctx, io, &cli, cli.Ports.Status, deps)
	case "update", "update <source>":
		return runUpdate(ctx, io, cli.Update)
	case "ssh-proxy <alias>", "ssh-proxy <alias> <port>":
		return runSSHProxy(ctx, io, &cli, cli.SshProxy, deps)
	default:
		return errfmt.Emit(io.Stderr, fmt.Errorf("unknown command %q", kctx.Command()))
	}
}

// maybeVersionExit handles `drift -v` / `drift --version` without needing a
// subcommand. Returns (exitCode, true) when the version flag was handled,
// (_, false) otherwise so normal Kong parsing proceeds.
func maybeVersionExit(argv []string, io IO) (int, bool) {
	hasVersion := false
	for _, a := range argv {
		if a == "--" {
			break
		}
		if a == "-v" || a == "--version" {
			hasVersion = true
			break
		}
	}
	if !hasVersion {
		return 0, false
	}
	return emitVersion(io, outputFromArgv(argv)), true
}

func emitVersion(io IO, outputFormat string) int {
	info := version.Get()
	if outputFormat == "json" {
		return emitJSON(io, info)
	}
	fmt.Fprintln(io.Stdout, info.Format("drift"))
	return 0
}

// outputFromArgv mirrors Kong's --output / -o parsing closely enough to pick
// the right format when we short-circuit the version flag before Kong runs.
// Unknown values fall through to "text" so a bad --output=foo on `drift -v`
// still prints something.
func outputFromArgv(argv []string) string {
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if a == "--" {
			break
		}
		if a == "--output" || a == "-o" {
			if i+1 < len(argv) {
				return argv[i+1]
			}
			continue
		}
		if v, ok := strings.CutPrefix(a, "--output="); ok {
			return v
		}
		if v, ok := strings.CutPrefix(a, "-o="); ok {
			return v
		}
	}
	return "text"
}
