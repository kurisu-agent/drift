package drift

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"

	"charm.land/huh/v2"
	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/cli/ui"
	driftexec "github.com/kurisu-agent/drift/internal/exec"
	"github.com/kurisu-agent/drift/internal/kart"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/wire"
)

// migrateCmd adopts one existing devpod workspace at a time as a drift
// kart. No flags, no batch mode — inherently interactive.
type migrateCmd struct{}

// migrateCandidate mirrors server.KartMigrateCandidate. Defining it
// locally avoids importing server from the client (the same convention
// every other drift CLI file follows).
type migrateCandidate struct {
	Name    string `json:"name"`
	Context string `json:"context"`
	Repo    string `json:"repo"`
}

type migrateListResponse struct {
	Candidates       []migrateCandidate `json:"candidates"`
	DefaultTune      string             `json:"default_tune,omitempty"`
	DefaultCharacter string             `json:"default_character,omitempty"`
}

// named is the narrow shape both tune.list and character.list return
// for our purposes — everything else in the response is ignored.
type named struct {
	Name string `json:"name"`
}

func runMigrate(ctx context.Context, io IO, root *CLI, _ migrateCmd, deps deps) int {
	// Interactive-only. Scripted automation that wants the same outcome
	// should compose kart.new directly; bundling the pickers into a
	// headless flag surface invites mismatches with the interactive path.
	if !stdinIsTTY(io.Stdin) || !stdoutIsTTY(io.Stdout) {
		return errfmt.Emit(io.Stderr,
			errors.New("drift migrate requires an interactive terminal"))
	}
	_, circuit, err := resolveCircuit(root, deps)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}

	var list migrateListResponse
	if err := deps.call(ctx, circuit, wire.MethodKartMigrateList, struct{}{}, &list); err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	if len(list.Candidates) == 0 {
		fmt.Fprintln(io.Stderr, "nothing to migrate")
		return 0
	}

	picked, ok, err := pickMigrateCandidate(list.Candidates)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	if !ok {
		return 0
	}

	tuneNames, characterNames, err := fetchTunesAndCharacters(ctx, deps, circuit)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}

	tune, ok, err := pickOneOf(
		"drift migrate — select a tune",
		"Tune = build/workspace/session config (env vars, dotfiles). type to filter · enter to pick · esc to cancel",
		tuneNames, list.DefaultTune)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	if !ok {
		return 0
	}
	character, ok, err := pickOneOf(
		"drift migrate — select a character",
		"Character = identity (git user + PAT) the kart commits and pushes as. type to filter · enter to pick · esc to cancel",
		characterNames, list.DefaultCharacter)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	if !ok {
		return 0
	}

	confirmed, err := confirmMigration(picked, tune, character)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	if !confirmed {
		return 0
	}

	kartName, ok, err := runKartNewForMigrate(ctx, io, deps, circuit, picked, tune, character)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	if !ok {
		return 0
	}

	fmt.Fprintf(io.Stdout, "migrated to kart %q\n", kartName)
	// The old devpod workspace in the user's ~/.devpod/ is left in place
	// — cleanup is the user's call, on their own devpod. Drift prints the
	// exact command so they don't have to remember the --context flag.
	printManualCleanup(io.Stderr, picked)
	return 0
}

// pickMigrateCandidate renders the filterable workspace list. Returns
// (picked, true, nil) on selection, (_, false, nil) on abort, err on
// fatal picker failure.
func pickMigrateCandidate(candidates []migrateCandidate) (migrateCandidate, bool, error) {
	opts := make([]huh.Option[int], 0, len(candidates))
	for i, c := range candidates {
		// Strip credentials embedded in clone URLs (e.g. https://<pat>@host/…)
		// before rendering — the picker is visible to anyone looking over
		// the user's shoulder. The raw value is preserved in `candidates`
		// and passed verbatim to kart.new.
		label := fmt.Sprintf("%s/%s    %s", c.Context, c.Name, driftexec.RedactSecrets(c.Repo))
		opts = append(opts, huh.NewOption(label, i))
	}
	var idx int
	sel := huh.NewSelect[int]().
		Title("drift migrate — choose a devpod workspace to adopt").
		Description("type to filter · enter to pick · esc to cancel").
		Options(opts...).
		Filtering(true).
		Height(18).
		Value(&idx)
	if err := huh.NewForm(huh.NewGroup(sel)).Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return migrateCandidate{}, false, nil
		}
		return migrateCandidate{}, false, err
	}
	return candidates[idx], true, nil
}

// fetchTunesAndCharacters parallelizes the two list calls. Runs
// sequentially right now — the deps.call surface doesn't expose a
// concurrent dispatcher, and the two RPCs combined are cheap enough that
// waiting serially is indistinguishable from parallel for the user.
func fetchTunesAndCharacters(ctx context.Context, deps deps, circuit string) (tunes, characters []string, err error) {
	var (
		tuneList []named
		charList []named
	)
	if err := deps.call(ctx, circuit, wire.MethodTuneList, struct{}{}, &tuneList); err != nil {
		return nil, nil, fmt.Errorf("tune.list: %w", err)
	}
	if err := deps.call(ctx, circuit, wire.MethodCharacterList, struct{}{}, &charList); err != nil {
		return nil, nil, fmt.Errorf("character.list: %w", err)
	}
	tunes = make([]string, 0, len(tuneList))
	for _, t := range tuneList {
		tunes = append(tunes, t.Name)
	}
	characters = make([]string, 0, len(charList))
	for _, c := range charList {
		characters = append(characters, c.Name)
	}
	return tunes, characters, nil
}

// pickOneOf renders a single-select over a string list with an optional
// pre-selected default. An empty options slice returns ("", true, nil)
// so callers skip the picker altogether and pass empty to kart.new (the
// server then decides based on its own defaults / fallbacks).
//
// title + desc are the huh Select title/description; callers pass both so
// the picker is self-describing (seeing just "default" in the list is
// ambiguous without a clear header saying what's being chosen).
func pickOneOf(title, desc string, options []string, def string) (string, bool, error) {
	if len(options) == 0 {
		return "", true, nil
	}
	huhOpts := make([]huh.Option[string], 0, len(options))
	for _, o := range options {
		huhOpts = append(huhOpts, huh.NewOption(o, o))
	}
	pick := def
	if !slices.Contains(options, pick) {
		pick = options[0]
	}
	sel := huh.NewSelect[string]().
		Title(title).
		Description(desc).
		Options(huhOpts...).
		Filtering(true).
		Height(10).
		Value(&pick)
	if err := huh.NewForm(huh.NewGroup(sel)).Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return "", false, nil
		}
		return "", false, err
	}
	return pick, true, nil
}

// confirmMigration summarizes the about-to-run kart.new before kicking
// it off so the user sees the resolved tune + character + source one
// last time.
func confirmMigration(c migrateCandidate, tune, character string) (bool, error) {
	var confirmed bool
	title := fmt.Sprintf("Create drift kart from %s/%s?", c.Context, c.Name)
	desc := fmt.Sprintf("source: %s\ntune: %s\ncharacter: %s",
		driftexec.RedactSecrets(c.Repo), cmp.Or(tune, "(none)"), cmp.Or(character, "(none)"))
	prompt := huh.NewConfirm().
		Title(title).
		Description(desc).
		Affirmative("migrate").
		Negative("cancel").
		Value(&confirmed)
	if err := huh.NewForm(huh.NewGroup(prompt)).Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return false, nil
		}
		return false, err
	}
	return confirmed, nil
}

// runKartNewForMigrate dispatches kart.new with a migrated_from
// back-reference and retries on name_collision by prompting for a new
// kart name. Returns (finalName, true, nil) on success, (_, false, nil)
// on user cancel, err on unrecoverable failure.
func runKartNewForMigrate(
	ctx context.Context,
	io IO,
	deps deps,
	circuit string,
	c migrateCandidate,
	tune, character string,
) (string, bool, error) {
	kartName := c.Name
	for {
		params := map[string]any{
			"name":  kartName,
			"clone": c.Repo,
			"migrated_from": map[string]string{
				"context": c.Context,
				"name":    c.Name,
			},
		}
		if tune != "" {
			params["tune"] = tune
		}
		if character != "" {
			params["character"] = character
		}
		var result kart.Result
		err := deps.call(ctx, circuit, wire.MethodKartNew, params, &result)
		if err == nil {
			return result.Name, true, nil
		}
		var re *rpcerr.Error
		if !errors.As(err, &re) || re.Type != rpcerr.TypeNameCollision {
			return "", false, err
		}
		suggestion := c.Context + "-" + c.Name
		newName, pErr := promptMigrateRename(io, kartName, suggestion)
		if pErr != nil {
			return "", false, pErr
		}
		if newName == "" {
			// empty = cancel
			return "", false, nil
		}
		kartName = newName
	}
}

// promptMigrateRename asks for a new kart name after name_collision.
// Reuses the visual style of promptNewKartName but adds a pre-filled
// suggestion so one keystroke accepts it.
func promptMigrateRename(io IO, taken, suggestion string) (string, error) {
	p := ui.NewTheme(io.Stderr, false)
	fmt.Fprintf(io.Stderr, "%s kart %q already exists on this circuit.\n",
		p.Warn("!"), taken)
	val := suggestion
	input := huh.NewInput().
		Title("pick a new kart name (blank cancels)").
		Value(&val).
		Validate(func(s string) error {
			// Empty is "cancel"; the outer loop handles it.
			return nil
		})
	if err := huh.NewForm(huh.NewGroup(input)).Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return "", nil
		}
		return "", err
	}
	return val, nil
}

// printManualCleanup dumps the exact command to run so the user doesn't
// have to remember devpod's context flag. Cleanup is the user's
// responsibility — drift never reaches into the user's ~/.devpod/ to
// delete on their behalf.
func printManualCleanup(w io.Writer, c migrateCandidate) {
	p := ui.NewTheme(w, false)
	fmt.Fprintln(w, p.Dim(fmt.Sprintf(
		"kept %s/%s — old workspace's state will diverge from the kart over time.",
		c.Context, c.Name)))
	fmt.Fprintln(w, p.Dim("delete it later with:"))
	if c.Context == "" || c.Context == "default" {
		fmt.Fprintf(w, "  devpod delete %s\n", c.Name)
	} else {
		fmt.Fprintf(w, "  devpod --context %s delete %s\n", c.Context, c.Name)
	}
}
