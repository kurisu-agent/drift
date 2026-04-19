// Package drift is the Kong CLI for the drift client binary.
package drift

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/alecthomas/kong"
	"github.com/kurisu-agent/drift/internal/version"
)

type CLI struct {
	Debug            bool   `help:"Verbose output." env:"DRIFT_DEBUG"`
	SkipVersionCheck bool   `name:"skip-version-check" help:"Bypass drift↔lakitu semver check."`
	Circuit          string `short:"c" help:"Target circuit (overrides default)."`
	Output           string `name:"output" help:"Output format for structured commands." enum:"text,json" default:"text"`

	Version  versionCmd `cmd:"" help:"Print drift version."`
	Help     helpCmd    `cmd:"" help:"Print an LLM-friendly command + protocol reference."`
	Circuit_ circuitCmd `cmd:"" name:"circuit" help:"Manage circuits (client-side config + SSH config)."`
	Warmup   warmupCmd  `cmd:"" name:"warmup" help:"Interactive first-time setup wizard (circuits + characters)."`
	New      newCmd     `cmd:"" name:"new" help:"Create a new kart (from starter or existing repo)."`

	List    listCmd    `cmd:"" help:"List karts on the target circuit."`
	Info    infoCmd    `cmd:"" help:"Show a single kart's info (JSON)."`
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

type versionCmd struct{}

type IO struct {
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader
}

func Run(ctx context.Context, argv []string, io IO) int {
	return run(ctx, argv, io, defaultDeps())
}

func run(ctx context.Context, argv []string, io IO, deps deps) int {
	var cli CLI
	parser, err := kong.New(&cli,
		kong.Name("drift"),
		kong.Description("drift — stateless client for remote devcontainer workspaces."),
		kong.Writers(io.Stdout, io.Stderr),
		kong.Exit(func(int) {}),
	)
	if err != nil {
		// Kong construction failures indicate a programmer mistake in the
		// CLI struct — don't route through errfmt.
		fmt.Fprintf(io.Stderr, "drift: %v\n", err)
		return 1
	}
	kctx, err := parser.Parse(argv)
	if err != nil {
		// Kong prints its own usage on Parse error — don't wrap in errfmt.
		fmt.Fprintf(io.Stderr, "drift: %v\n", err)
		return 2
	}
	switch kctx.Command() {
	case "version":
		return runVersion(io, cli.Output)
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
	case "warmup":
		return runWarmup(ctx, io, &cli, cli.Warmup, deps)
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
		return emitError(io, fmt.Errorf("unknown command %q", kctx.Command()))
	}
}

func runVersion(io IO, outputFormat string) int {
	info := version.Get()
	switch outputFormat {
	case "json":
		buf, err := json.Marshal(info)
		if err != nil {
			return emitError(io, err)
		}
		fmt.Fprintln(io.Stdout, string(buf))
	default:
		fmt.Fprintf(io.Stdout, "drift %s\n", info.Version)
	}
	return 0
}
