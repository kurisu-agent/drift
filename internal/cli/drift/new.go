package drift

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/kurisu-agent/drift/internal/config"
	"github.com/kurisu-agent/drift/internal/kart"
	"github.com/kurisu-agent/drift/internal/rpc/client"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/wire"
)

// newCmd is the Kong command for `drift new <name>`. plans/PLAN.md § drift
// new flags enumerates the flags; this struct stays 1:1 with that list so
// the drift client and the kart.new RPC surface never drift apart.
type newCmd struct {
	Name         string `arg:"" help:"Kart name (matches ^[a-z][a-z0-9-]{0,62}$)."`
	Clone        string `name:"clone" help:"Clone an existing repo (mutually exclusive with --starter)."`
	Starter      string `name:"starter" help:"Template repo; history is discarded after clone."`
	Tune         string `name:"tune" help:"Named preset that provides defaults for other flags."`
	Features     string `name:"features" help:"Devcontainer features JSON, merged with the tune's (additive)."`
	Devcontainer string `name:"devcontainer" help:"Override devcontainer: file path, JSON string, or URL."`
	Dotfiles     string `name:"dotfiles" help:"Layer-2 dotfiles repo URL (overrides tune's dotfiles_repo)."`
	Character    string `name:"character" help:"Git/GitHub identity to inject."`
	Autostart    bool   `name:"autostart" help:"Enable auto-start on server reboot."`
}

// runNew issues the kart.new RPC against the resolved circuit.
func runNew(ctx context.Context, io IO, root *CLI, cmd newCmd, deps deps) int {
	if cmd.Clone != "" && cmd.Starter != "" {
		return emitError(io, errors.New("--clone and --starter are mutually exclusive"))
	}

	circuit, err := resolveCircuit(root, deps)
	if err != nil {
		return emitError(io, err)
	}

	params := map[string]any{"name": cmd.Name}
	if cmd.Clone != "" {
		params["clone"] = cmd.Clone
	}
	if cmd.Starter != "" {
		params["starter"] = cmd.Starter
	}
	if cmd.Tune != "" {
		params["tune"] = cmd.Tune
	}
	if cmd.Features != "" {
		params["features"] = cmd.Features
	}
	if cmd.Devcontainer != "" {
		params["devcontainer"] = cmd.Devcontainer
	}
	if cmd.Dotfiles != "" {
		params["dotfiles"] = cmd.Dotfiles
	}
	if cmd.Character != "" {
		params["character"] = cmd.Character
	}
	if cmd.Autostart {
		params["autostart"] = true
	}

	rpcc := client.New()
	var result kart.Result
	if err := rpcc.Call(ctx, circuit, wire.MethodKartNew, params, &result); err != nil {
		return emitRPCError(io, err)
	}

	if root.Output == "json" {
		buf, mErr := json.Marshal(result)
		if mErr != nil {
			return emitError(io, mErr)
		}
		fmt.Fprintln(io.Stdout, string(buf))
		return 0
	}
	fmt.Fprintf(io.Stdout, "created kart %q\n", result.Name)
	if result.Source.Mode != "none" && result.Source.URL != "" {
		fmt.Fprintf(io.Stdout, "  source: %s (%s)\n", result.Source.URL, result.Source.Mode)
	}
	if result.Tune != "" {
		fmt.Fprintf(io.Stdout, "  tune: %s\n", result.Tune)
	}
	if result.Character != "" {
		fmt.Fprintf(io.Stdout, "  character: %s\n", result.Character)
	}
	if result.Autostart {
		fmt.Fprintln(io.Stdout, "  autostart: enabled")
	}
	if result.Warning != "" {
		fmt.Fprintf(io.Stderr, "warning: %s\n", result.Warning)
	}
	return 0
}

// resolveCircuit picks the target circuit: the global --circuit flag wins,
// falling back to the client config's default_circuit. Errors when no
// circuit is configured so users see a clear next step instead of an SSH
// DNS failure.
func resolveCircuit(root *CLI, deps deps) (string, error) {
	if root.Circuit != "" {
		return root.Circuit, nil
	}
	cfgPath, err := deps.clientConfigPath()
	if err != nil {
		return "", err
	}
	cfg, err := config.LoadClient(cfgPath)
	if err != nil {
		return "", err
	}
	if cfg.DefaultCircuit == "" {
		return "", errors.New("no circuit specified (use --circuit or set a default via `drift circuit add --default`)")
	}
	return cfg.DefaultCircuit, nil
}

// emitRPCError renders a transport or RPC error with plans/PLAN.md's
// stderr format when possible. Transport failures carry the original SSH
// stderr; RPC errors are the typed rpcerr.
func emitRPCError(io IO, err error) int {
	var re *rpcerr.Error
	if errors.As(err, &re) {
		fmt.Fprintf(io.Stderr, "error: %s\n", re.Message)
		buf, mErr := json.Marshal(re)
		if mErr == nil {
			fmt.Fprintln(io.Stderr, string(buf))
		}
		return int(re.Code)
	}
	var te *client.TransportError
	if errors.As(err, &te) {
		fmt.Fprintf(io.Stderr, "error: %s\n", te.Error())
		return 1
	}
	fmt.Fprintf(io.Stderr, "error: %v\n", err)
	return 1
}
