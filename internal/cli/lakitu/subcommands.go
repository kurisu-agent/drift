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

// Routing every human subcommand through the registry keeps the RPC and
// human paths exercising the same dispatch code — stdout-invariant
// coverage carries over for free.

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

// Character CLI — new/set/unset/edit/show/list/rm. `set` and `unset`
// take a dotted field path; flags are only on `new` (creation time).
type characterCmd struct {
	New    characterNewCmd    `cmd:"" help:"Create a character."`
	Set    characterSetCmd    `cmd:"" help:"Set a field on an existing character (git_email, github_user, …)."`
	Unset  characterUnsetCmd  `cmd:"" help:"Clear a field on an existing character."`
	Edit   characterEditCmd   `cmd:"" help:"Open the character YAML in $EDITOR."`
	List   characterListCmd   `cmd:"" name:"list" help:"List characters."`
	Show   characterShowCmd   `cmd:"" help:"Show a character."`
	Remove characterRemoveCmd `cmd:"" name:"rm" help:"Remove a character."`
}

type characterNewCmd struct {
	Name       string `arg:""`
	GitName    string `name:"git-name" required:"" help:"Git committer name (user.name)."`
	GitEmail   string `name:"git-email" required:"" help:"Git committer email (user.email)."`
	GithubUser string `name:"github-user" help:"GitHub handle for gh CLI auth inside karts."`
	SSHKeyPath string `name:"ssh-key-path" help:"Path to the SSH private key to mount into karts."`
	PATSecret  string `name:"pat-secret" help:"Chest reference of the form chest:<name>."`
}

type characterSetCmd struct {
	Name  string `arg:"" help:"Character name."`
	Field string `arg:"" help:"Dotted field path (git_email, github_user, …)."`
	Value string `arg:"" help:"New value."`
}

type characterUnsetCmd struct {
	Name  string `arg:"" help:"Character name."`
	Field string `arg:"" help:"Dotted field path to clear."`
}

type characterEditCmd struct {
	Name string `arg:"" help:"Character name."`
}

type characterListCmd struct{}
type characterShowCmd struct {
	Name string `arg:""`
}
type characterRemoveCmd struct {
	Name string `arg:""`
}

// Tune CLI — same verb shape as character. Flags on `new` cover the
// common creation-time fields; env / mount_dirs are reachable via
// `set` (dotted path) or `edit`.
type tuneCmd struct {
	New    tuneNewCmd    `cmd:"" help:"Create a tune."`
	Set    tuneSetCmd    `cmd:"" help:"Set a field on an existing tune (starter, env.build.X, …)."`
	Unset  tuneUnsetCmd  `cmd:"" help:"Clear a field on an existing tune."`
	Edit   tuneEditCmd   `cmd:"" help:"Open the tune YAML in $EDITOR."`
	List   tuneListCmd   `cmd:"" name:"list" help:"List tunes."`
	Show   tuneShowCmd   `cmd:"" help:"Show a tune."`
	Remove tuneRemoveCmd `cmd:"" name:"rm" help:"Remove a tune."`
}

type tuneNewCmd struct {
	Name         string `arg:""`
	Starter      string `name:"starter" help:"Starter repo URL (git or file://). Cloned into new karts that pick this tune."`
	Devcontainer string `name:"devcontainer" help:"Path to a devcontainer.json fragment merged into the starter."`
	DotfilesRepo string `name:"dotfiles-repo" help:"Dotfiles repo URL applied on kart creation."`
	Features     string `name:"features" help:"Raw JSON passed through to devpod --additional-features."`
}

type tuneSetCmd struct {
	Name  string `arg:"" help:"Tune name."`
	Field string `arg:"" help:"Dotted field path (starter, env.build.GITHUB_TOKEN, …)."`
	Value string `arg:"" help:"New value."`
}

type tuneUnsetCmd struct {
	Name  string `arg:"" help:"Tune name."`
	Field string `arg:"" help:"Dotted field path to clear."`
}

type tuneEditCmd struct {
	Name string `arg:"" help:"Tune name."`
}

type tuneListCmd struct{}
type tuneShowCmd struct {
	Name string `arg:""`
}
type tuneRemoveCmd struct {
	Name string `arg:""`
}

// Chest CLI — `new` for create (errors if exists), `set` for update,
// both read the value from stdin.
type chestCmd struct {
	New    chestNewCmd    `cmd:"" help:"Store a new secret (value read from stdin; errors if name exists)."`
	Set    chestSetCmd    `cmd:"" help:"Update an existing secret (value read from stdin)."`
	Get    chestGetCmd    `cmd:"" help:"Print a secret's value."`
	List   chestListCmd   `cmd:"" name:"list" help:"List secret names."`
	Remove chestRemoveCmd `cmd:"" name:"rm" help:"Remove a secret."`
}

type chestNewCmd struct {
	Name string `arg:""`
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

func runConfigShow(ctx context.Context, io IO) int {
	return callAndPrint(ctx, io, wire.MethodConfigShow, struct{}{})
}

func runConfigSet(ctx context.Context, io IO, cmd configSetCmd) int {
	return callAndPrint(ctx, io, wire.MethodConfigSet, server.ConfigSetParams{
		Key:   cmd.Key,
		Value: cmd.Value,
	})
}

func runCharacterNew(ctx context.Context, io IO, cmd characterNewCmd) int {
	return callAndPrint(ctx, io, wire.MethodCharacterNew, server.CharacterNewParams{
		Name:       cmd.Name,
		GitName:    cmd.GitName,
		GitEmail:   cmd.GitEmail,
		GithubUser: cmd.GithubUser,
		SSHKeyPath: cmd.SSHKeyPath,
		PATSecret:  cmd.PATSecret,
	})
}

func runCharacterSet(ctx context.Context, io IO, cmd characterSetCmd) int {
	return callAndPrint(ctx, io, wire.MethodCharacterPatch, server.CharacterPatchParams{
		Name: cmd.Name,
		Ops:  []server.CharacterPatchOp{{Path: cmd.Field, Op: "set", Value: cmd.Value}},
	})
}

func runCharacterUnset(ctx context.Context, io IO, cmd characterUnsetCmd) int {
	return callAndPrint(ctx, io, wire.MethodCharacterPatch, server.CharacterPatchParams{
		Name: cmd.Name,
		Ops:  []server.CharacterPatchOp{{Path: cmd.Field, Op: "unset"}},
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

func runTuneNew(ctx context.Context, io IO, cmd tuneNewCmd) int {
	return callAndPrint(ctx, io, wire.MethodTuneNew, server.TuneNewParams{
		Name:         cmd.Name,
		Starter:      cmd.Starter,
		Devcontainer: cmd.Devcontainer,
		DotfilesRepo: cmd.DotfilesRepo,
		Features:     cmd.Features,
	})
}

func runTuneSet(ctx context.Context, io IO, cmd tuneSetCmd) int {
	return callAndPrint(ctx, io, wire.MethodTunePatch, server.TunePatchParams{
		Name: cmd.Name,
		Ops:  []server.TunePatchOp{{Path: cmd.Field, Op: "set", Value: cmd.Value}},
	})
}

func runTuneUnset(ctx context.Context, io IO, cmd tuneUnsetCmd) int {
	return callAndPrint(ctx, io, wire.MethodTunePatch, server.TunePatchParams{
		Name: cmd.Name,
		Ops:  []server.TunePatchOp{{Path: cmd.Field, Op: "unset"}},
	})
}

func runTuneList(ctx context.Context, io IO) int {
	return callAndPrint(ctx, io, wire.MethodTuneList, struct{}{})
}

func runTuneShow(ctx context.Context, io IO, cmd tuneShowCmd) int {
	return callAndPrint(ctx, io, wire.MethodTuneShow, server.TuneNameOnly{Name: cmd.Name})
}

func runTuneRemove(ctx context.Context, io IO, cmd tuneRemoveCmd) int {
	return callAndPrint(ctx, io, wire.MethodTuneRemove, server.TuneNameOnly{Name: cmd.Name})
}

// runChestNew / runChestSet read stdin (never a flag). Stripping one
// trailing newline (POSIX pipe convention) lets embedded newlines
// survive for multi-line secrets. new errors if exists; set errors
// if missing.
func runChestNew(ctx context.Context, io IO, cmd chestNewCmd) int {
	value, err := readChestStdin(io)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	return callAndPrint(ctx, io, wire.MethodChestNew, server.ChestPutParams{
		Name:  cmd.Name,
		Value: value,
	})
}

func runChestSet(ctx context.Context, io IO, cmd chestSetCmd) int {
	value, err := readChestStdin(io)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	return callAndPrint(ctx, io, wire.MethodChestPatch, server.ChestPutParams{
		Name:  cmd.Name,
		Value: value,
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

func readChestStdin(iob IO) (string, error) {
	value, err := iob.ReadAll()
	if err != nil {
		return "", fmt.Errorf("read stdin: %w", err)
	}
	if n := len(value); n > 0 && value[n-1] == '\n' {
		value = value[:n-1]
	}
	return string(value), nil
}

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
	// Only materialize the pinned devpod when the method needs it —
	// `lakitu character list` shouldn't trigger a 117MB download on a
	// fresh circuit.
	needDevpod := methodNeedsDevpod(method)
	reg, regErr := buildRegistry(ctx, needDevpod, false)
	if regErr != nil && needDevpod {
		return errfmt.Emit(io.Stderr, regErr)
	}
	resp := reg.Dispatch(ctx, req)
	if resp.Error != nil {
		return errfmt.Emit(io.Stderr, rpcerr.FromWire(resp.Error))
	}
	// Pretty-print for humans; machine callers use `lakitu rpc` for the
	// raw envelope.
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

func (iob IO) ReadAll() ([]byte, error) {
	if iob.Stdin == nil {
		return nil, nil
	}
	return io.ReadAll(iob.Stdin)
}
