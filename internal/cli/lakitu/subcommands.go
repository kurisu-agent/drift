package lakitu

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/server"
	"github.com/kurisu-agent/drift/internal/wire"
)

// Kong command types for the human CLI surface. Each command parses argv
// into the same params struct the RPC handlers consume, dispatches through
// the registry, and formats the result for the terminal.
//
// Using the registry here (rather than calling handler functions directly)
// keeps the RPC path and the human path exercising the same dispatch code —
// the stdout invariant test in cliscript protects the RPC side; the human
// side gets the same behavior for free.

type kartListCmd struct{}

type kartInfoCmd struct {
	Name string `arg:"" help:"Kart name."`
}

func runKartList(ctx context.Context, io IO) int {
	return callAndPrint(ctx, io, wire.MethodKartList, struct{}{})
}

func runKartInfo(ctx context.Context, io IO, cmd kartInfoCmd) int {
	return callAndPrint(ctx, io, wire.MethodKartInfo, server.KartInfoParams{Name: cmd.Name})
}

type configCmd struct {
	Show configShowCmd `cmd:"" help:"Print the server config."`
	Set  configSetCmd  `cmd:"" help:"Set a server config key."`
}

type configShowCmd struct{}

type configSetCmd struct {
	Key   string `arg:"" help:"Dotted key: default_tune, default_character, nix_cache_url, chest.backend."`
	Value string `arg:"" help:"New value."`
}

type characterCmd struct {
	Add    characterAddCmd    `cmd:"" help:"Add a character."`
	List   characterListCmd   `cmd:"" name:"list" help:"List characters."`
	Show   characterShowCmd   `cmd:"" help:"Show a character."`
	Remove characterRemoveCmd `cmd:"" name:"rm" help:"Remove a character."`
}

type characterAddCmd struct {
	Name       string `arg:""`
	GitName    string `name:"git-name" required:""`
	GitEmail   string `name:"git-email" required:""`
	GithubUser string `name:"github-user"`
	SSHKeyPath string `name:"ssh-key-path"`
	PATSecret  string `name:"pat-secret" help:"Chest reference of the form chest:<name>."`
}

type characterListCmd struct{}
type characterShowCmd struct {
	Name string `arg:""`
}
type characterRemoveCmd struct {
	Name string `arg:""`
}

type tuneCmd struct {
	List   tuneListCmd   `cmd:"" name:"list" help:"List tunes."`
	Show   tuneShowCmd   `cmd:"" help:"Show a tune."`
	Set    tuneSetCmd    `cmd:"" help:"Create or update a tune."`
	Remove tuneRemoveCmd `cmd:"" name:"rm" help:"Remove a tune."`
}

type tuneListCmd struct{}
type tuneShowCmd struct {
	Name string `arg:""`
}
type tuneSetCmd struct {
	Name         string `arg:""`
	Starter      string `name:"starter"`
	Devcontainer string `name:"devcontainer"`
	DotfilesRepo string `name:"dotfiles-repo"`
	Features     string `name:"features" help:"Raw JSON passed through to devpod --additional-features."`
}
type tuneRemoveCmd struct {
	Name string `arg:""`
}

type chestCmd struct {
	Set    chestSetCmd    `cmd:"" help:"Store a secret (value read from stdin)."`
	Get    chestGetCmd    `cmd:"" help:"Print a secret's value."`
	List   chestListCmd   `cmd:"" name:"list" help:"List secret names."`
	Remove chestRemoveCmd `cmd:"" name:"rm" help:"Remove a secret."`
}

type chestSetCmd struct {
	Name string `arg:""`
}
type chestGetCmd struct {
	Name string `arg:""`
}
type chestListCmd struct{}
type chestRemoveCmd struct {
	Name string `arg:""`
}

// runConfigShow dispatches config.show through the registry and prints the
// JSON result to stdout.
func runConfigShow(ctx context.Context, io IO) int {
	return callAndPrint(ctx, io, wire.MethodConfigShow, struct{}{})
}

func runConfigSet(ctx context.Context, io IO, cmd configSetCmd) int {
	return callAndPrint(ctx, io, wire.MethodConfigSet, server.ConfigSetParams{
		Key:   cmd.Key,
		Value: cmd.Value,
	})
}

func runCharacterAdd(ctx context.Context, io IO, cmd characterAddCmd) int {
	return callAndPrint(ctx, io, wire.MethodCharacterAdd, server.CharacterAddParams{
		Name:       cmd.Name,
		GitName:    cmd.GitName,
		GitEmail:   cmd.GitEmail,
		GithubUser: cmd.GithubUser,
		SSHKeyPath: cmd.SSHKeyPath,
		PATSecret:  cmd.PATSecret,
	})
}

func runCharacterList(ctx context.Context, io IO) int {
	return callAndPrint(ctx, io, wire.MethodCharacterList, struct{}{})
}

func runCharacterShow(ctx context.Context, io IO, cmd characterShowCmd) int {
	return callAndPrint(ctx, io, wire.MethodCharacterShow, server.CharacterNameOnly{Name: cmd.Name})
}

func runCharacterRemove(ctx context.Context, io IO, cmd characterRemoveCmd) int {
	return callAndPrint(ctx, io, wire.MethodCharacterRemove, server.CharacterNameOnly{Name: cmd.Name})
}

func runTuneList(ctx context.Context, io IO) int {
	return callAndPrint(ctx, io, wire.MethodTuneList, struct{}{})
}

func runTuneShow(ctx context.Context, io IO, cmd tuneShowCmd) int {
	return callAndPrint(ctx, io, wire.MethodTuneShow, server.TuneNameOnly{Name: cmd.Name})
}

func runTuneSet(ctx context.Context, io IO, cmd tuneSetCmd) int {
	return callAndPrint(ctx, io, wire.MethodTuneSet, server.TuneSetParams{
		Name:         cmd.Name,
		Starter:      cmd.Starter,
		Devcontainer: cmd.Devcontainer,
		DotfilesRepo: cmd.DotfilesRepo,
		Features:     cmd.Features,
	})
}

func runTuneRemove(ctx context.Context, io IO, cmd tuneRemoveCmd) int {
	return callAndPrint(ctx, io, wire.MethodTuneRemove, server.TuneNameOnly{Name: cmd.Name})
}

// runChestSet reads the secret value from stdin (never a flag) and
// dispatches chest.set. One trailing newline is stripped — the usual POSIX
// convention for piped secrets — but embedded newlines survive untouched
// for callers that need multi-line values.
func runChestSet(ctx context.Context, io IO, cmd chestSetCmd) int {
	value, err := io.ReadAll()
	if err != nil {
		return errfmt.Emit(io.Stderr, fmt.Errorf("read stdin: %w", err))
	}
	if n := len(value); n > 0 && value[n-1] == '\n' {
		value = value[:n-1]
	}
	return callAndPrint(ctx, io, wire.MethodChestSet, server.ChestSetParams{
		Name:  cmd.Name,
		Value: string(value),
	})
}

func runChestGet(ctx context.Context, io IO, cmd chestGetCmd) int {
	return callAndPrint(ctx, io, wire.MethodChestGet, server.ChestNameOnly{Name: cmd.Name})
}

func runChestList(ctx context.Context, io IO) int {
	return callAndPrint(ctx, io, wire.MethodChestList, struct{}{})
}

func runChestRemove(ctx context.Context, io IO, cmd chestRemoveCmd) int {
	return callAndPrint(ctx, io, wire.MethodChestRemove, server.ChestNameOnly{Name: cmd.Name})
}

// callAndPrint is the shared in-process dispatch helper for every human
// subcommand. It marshals params, runs them through the live [*rpc.Registry],
// and then renders the response — either the JSON result on stdout or the
// structured error on stderr via errfmt.Emit (plans/PLAN.md § "stderr format").
func callAndPrint(ctx context.Context, io IO, method string, params any) int {
	raw, err := json.Marshal(params)
	if err != nil {
		return errfmt.Emit(io.Stderr, fmt.Errorf("marshal params: %w", err))
	}
	req := &wire.Request{
		JSONRPC: wire.Version,
		Method:  method,
		Params:  raw,
		ID:      json.RawMessage(`1`),
	}
	resp := Registry().Dispatch(ctx, req)
	if resp.Error != nil {
		return errfmt.Emit(io.Stderr, rpcerr.FromWire(resp.Error))
	}
	// Result is already JSON. Pretty-print for humans; machine callers use
	// `lakitu rpc` for the raw envelope anyway.
	var v any
	if err := json.Unmarshal(resp.Result, &v); err != nil {
		return errfmt.Emit(io.Stderr, fmt.Errorf("decode result: %w", err))
	}
	pretty, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return errfmt.Emit(io.Stderr, fmt.Errorf("encode result: %w", err))
	}
	fmt.Fprintln(io.Stdout, string(pretty))
	return 0
}

// ReadAll pulls every byte from IO.Stdin. Centralized so tests can swap in
// a bytes.Buffer and get the same behavior as a real terminal pipe.
func (iob IO) ReadAll() ([]byte, error) {
	if iob.Stdin == nil {
		return nil, nil
	}
	return io.ReadAll(iob.Stdin)
}
