// Package drift is the Kong CLI for the drift client binary.
package drift

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/version"
)

type CLI struct {
	Debug            bool   `help:"Verbose output." env:"DRIFT_DEBUG"`
	SkipVersionCheck bool   `name:"skip-version-check" hidden:"" env:"DRIFT_SKIP_VERSION_CHECK" help:"Bypass drift↔lakitu semver check."`
	NoColor          bool   `name:"no-color" env:"NO_COLOR" help:"Disable ANSI colors in text output."`
	Circuit          string `short:"c" help:"Target circuit (overrides default)."`
	Output           string `name:"output" short:"o" help:"Output format for structured commands." enum:"text,json" default:"text"`

	// Version is scanned out of argv before Kong parses, so this field is
	// never read — it exists purely to register `-v` / `--version` in the
	// help output Kong auto-generates. See maybeVersionExit.
	Version bool `short:"v" help:"Print drift version and exit."`

	Help     helpCmd    `cmd:"" help:"Print an LLM-friendly command + protocol reference."`
	Circuit_ circuitCmd `cmd:"" name:"circuit" help:"Manage circuits (client-side config + SSH config)."`
	Init     initCmd    `cmd:"" name:"init" help:"Interactive first-time setup wizard (circuits + characters)."`
	Status   statusCmd  `cmd:"" name:"status" help:"Show configured circuits + their lakitu health and kart counts."`
	New      newCmd     `cmd:"" name:"new" help:"Create a new kart (from starter or existing repo)."`

	List    listCmd    `cmd:"" help:"List karts on the target circuit."`
	Info    infoCmd    `cmd:"" help:"Show a single kart's info."`
	Start   startCmd   `cmd:"" help:"Start a kart (idempotent)."`
	Stop    stopCmd    `cmd:"" help:"Stop a kart (idempotent)."`
	Restart restartCmd `cmd:"" help:"Restart a kart."`
	Delete  deleteCmd  `cmd:"" help:"Delete a kart (errors if missing)."`
	Logs    logsCmd    `cmd:"" help:"Fetch a chunk of kart logs."`
	Enable  enableCmd  `cmd:"" help:"Enable kart autostart on circuit reboot (idempotent)."`
	Disable disableCmd `cmd:"" help:"Disable kart autostart (idempotent)."`
	Connect connectCmd `cmd:"" help:"Connect to a kart via mosh (ssh fallback); auto-starts if stopped."`
	AI      aiCmd      `cmd:"" name:"ai" help:"Launch claude --dangerously-skip-permissions on the circuit (mosh/ssh)."`

	Update updateCmd `cmd:"" name:"update" help:"Check GitHub for a newer drift release and self-install."`

	SshProxy sshProxyCmd `cmd:"" name:"ssh-proxy" hidden:"" help:"ProxyCommand helper for drift.<circuit>.<kart> aliases (invoked by OpenSSH)."`
}

type IO struct {
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader
}

func Run(ctx context.Context, argv []string, io IO) int {
	return run(ctx, argv, io, defaultDeps())
}

func run(ctx context.Context, argv []string, io IO, deps deps) int {
	// Intercept -v / --version before Kong's command-required parser rejects
	// a flag-only invocation. Output format is scraped from the same argv so
	// `drift -v --output json` still produces machine output.
	if rc, handled := maybeVersionExit(argv, io); handled {
		return rc
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

	switch kctx.Command() {
	case "help":
		return runHelp(io, parser)
	case "circuit add <name>":
		return runCircuitAdd(ctx, io, &cli, cli.Circuit_.Add, deps)
	case "circuit rm <name>":
		return runCircuitRm(io, &cli, cli.Circuit_.Rm, deps)
	case "circuit list":
		return runCircuitList(io, &cli, deps)
	case "new <name>":
		return runNew(ctx, io, &cli, cli.New, deps)
	case "list":
		return runKartList(ctx, io, &cli, cli.List, deps)
	case "info <name>":
		return runKartInfo(ctx, io, &cli, cli.Info, deps)
	case "init":
		return runInit(ctx, io, &cli, cli.Init, deps)
	case "status":
		return runStatus(ctx, io, &cli, cli.Status, deps)
	case "start <name>":
		return runKartStart(ctx, io, &cli, cli.Start, deps)
	case "stop <name>":
		return runKartStop(ctx, io, &cli, cli.Stop, deps)
	case "restart <name>":
		return runKartRestart(ctx, io, &cli, cli.Restart, deps)
	case "delete <name>":
		return runKartDelete(ctx, io, &cli, cli.Delete, deps)
	case "logs <name>":
		return runKartLogs(ctx, io, &cli, cli.Logs, deps)
	case "enable <name>":
		return runKartEnable(ctx, io, &cli, cli.Enable, deps)
	case "disable <name>":
		return runKartDisable(ctx, io, &cli, cli.Disable, deps)
	case "connect <name>":
		return runConnect(ctx, io, &cli, cli.Connect, deps)
	case "ai":
		return runAI(ctx, io, &cli, cli.AI, deps)
	case "update":
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
		buf, err := json.Marshal(info)
		if err != nil {
			return errfmt.Emit(io.Stderr, err)
		}
		fmt.Fprintln(io.Stdout, string(buf))
		return 0
	}
	fmt.Fprintf(io.Stdout, "drift %s\n", info.Version)
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
