package drift

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/kart"
	"github.com/kurisu-agent/drift/internal/rpc/client"
	"github.com/kurisu-agent/drift/internal/wire"
)

// newCmd is the Kong command for `drift new <name>`. This struct stays 1:1
// with the `drift new` flag list so the drift client and the kart.new RPC
// surface never drift apart.
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

// emitRPCError routes both RPC-level and transport errors through errfmt.
// A *rpcerr.Error produces the full two-line format with exit code mirroring
// Code; a *client.TransportError (SSH exited non-zero before any envelope
// was read) is rendered via the untyped fallback — the message is ssh's own
// stderr passed through verbatim, with exit code 1.
func emitRPCError(io IO, err error) int {
	return errfmt.Emit(io.Stderr, err)
}
