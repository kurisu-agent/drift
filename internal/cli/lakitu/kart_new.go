package lakitu

import (
	"context"

	"github.com/kurisu-agent/drift/internal/server"
	"github.com/kurisu-agent/drift/internal/wire"
)

// kartCmd groups kart-specific subcommands that weren't already
// fronted by the flat `lakitu list` / `lakitu info` shortcuts.
type kartCmd struct {
	New kartNewCmd `cmd:"" help:"Create a new kart (same handler drift new uses)."`
}

// kartNewCmd mirrors drift's newCmd — same flag names, same RPC — so an
// agent launched via `drift ai` / `drift skill` can invoke `lakitu kart
// new …` identically to how a user would invoke `drift new …` on the
// workstation.
type kartNewCmd struct {
	Name            string `arg:"" help:"Kart name (matches ^[a-z][a-z0-9-]{0,62}$)."`
	Clone           string `name:"clone" help:"Clone an existing repo (mutually exclusive with --starter)."`
	Starter         string `name:"starter" help:"Template repo; history is discarded after clone."`
	Tune            string `name:"tune" help:"Named preset that provides defaults for other flags."`
	Features        string `name:"features" help:"Devcontainer features JSON, merged with the tune's (additive)."`
	Devcontainer    string `name:"devcontainer" help:"Override devcontainer: file path, JSON string, or URL."`
	Dotfiles        string `name:"dotfiles" help:"Layer-2 dotfiles repo URL (overrides tune's dotfiles_repo)."`
	Character       string `name:"character" help:"Git/GitHub identity to inject."`
	Autostart       bool   `name:"autostart" help:"Enable auto-start on server reboot."`
	NoNormaliseUser bool   `name:"no-normalise-user" help:"Skip renaming the image's default non-root user to the character (overrides tune's normalise_user)."`
}

func runKartNewLocal(ctx context.Context, io IO, cmd kartNewCmd) int {
	params := server.KartNewParams{
		Name:         cmd.Name,
		Clone:        cmd.Clone,
		Starter:      cmd.Starter,
		Tune:         cmd.Tune,
		Features:     cmd.Features,
		Devcontainer: cmd.Devcontainer,
		Dotfiles:     cmd.Dotfiles,
		Character:    cmd.Character,
		Autostart:    cmd.Autostart,
	}
	if cmd.NoNormaliseUser {
		off := false
		params.NormaliseUser = &off
	}
	return callAndPrint(ctx, io, wire.MethodKartNew, params)
}
