package drift

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"

	"github.com/charmbracelet/huh"
)

// kartNameRe matches the server-side validator
// (^[a-z][a-z0-9-]{0,62}$). Duplicating it client-side gives the wizard
// instant feedback instead of waiting for the RPC to bounce.
var kartNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// runNewWizard fills cmd by walking the user through the kart.new
// inputs interactively. Returns (aborted, err) — aborted=true means the
// user pressed esc or ctrl+c; the caller should treat it as a clean
// "user said no" and exit without an error message. err is reserved for
// fatal failures (RPC blowups, prompt library errors).
//
// Existing values on cmd are honoured as defaults so flag-and-wizard
// hybrid invocations (`drift new --tune=foo` to pre-pick the tune)
// don't lose information.
func runNewWizard(ctx context.Context, io IO, deps deps, circuit string, cmd *newCmd) (bool, error) {
	tunes, characters, lerr := fetchTunesAndCharacters(ctx, deps, circuit)
	if lerr != nil {
		return false, lerr
	}

	if aborted, err := promptKartName(cmd); err != nil || aborted {
		return aborted, err
	}
	if aborted, err := promptKartSource(cmd); err != nil || aborted {
		return aborted, err
	}
	if aborted, err := promptKartTune(cmd, tunes); err != nil || aborted {
		return aborted, err
	}
	if aborted, err := promptKartCharacter(cmd, characters); err != nil || aborted {
		return aborted, err
	}
	if aborted, err := promptKartAutostart(cmd); err != nil || aborted {
		return aborted, err
	}

	// Pick up shorthand again now that the wizard may have written
	// cmd.Clone — the same `owner/repo → https://github.com/...`
	// expansion the flag path enjoys.
	expandCloneShorthand(cmd)

	// PAT auto-detect picker still runs from runNew once the wizard
	// returns; keeping it there means the same code path serves
	// `drift new <name> --clone …` and the wizard.
	return false, nil
}

func promptKartName(cmd *newCmd) (bool, error) {
	val := cmd.Name
	if val == "" {
		val = suggestKartName(cmd)
	}
	input := huh.NewInput().
		Title("kart name").
		Description("lowercase letters, digits, and dashes; up to 63 chars.").
		Value(&val).
		Validate(func(s string) error {
			s = strings.TrimSpace(s)
			if s == "" {
				return errors.New("name is required")
			}
			if !kartNameRe.MatchString(s) {
				return errors.New("must match ^[a-z][a-z0-9-]{0,62}$")
			}
			return nil
		})
	if err := huh.NewForm(huh.NewGroup(input)).Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return true, nil
		}
		return false, err
	}
	cmd.Name = strings.TrimSpace(val)
	return false, nil
}

// kartSourceMode is the wizard's source picker tri-state. None skips the
// repo input entirely; clone and starter both gate a follow-up input.
type kartSourceMode int

const (
	sourceNone kartSourceMode = iota
	sourceClone
	sourceStarter
)

func promptKartSource(cmd *newCmd) (bool, error) {
	mode := initialSourceMode(cmd)
	sel := huh.NewSelect[kartSourceMode]().
		Title("repo source").
		Description("none = empty workspace · clone = bring your existing repo · starter = template (history discarded)").
		Options(
			huh.NewOption("none — empty workspace", sourceNone),
			huh.NewOption("clone — existing repo", sourceClone),
			huh.NewOption("starter — template", sourceStarter),
		).
		Value(&mode)
	if err := huh.NewForm(huh.NewGroup(sel)).Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return true, nil
		}
		return false, err
	}

	switch mode {
	case sourceNone:
		cmd.Clone = ""
		cmd.Starter = ""
		return false, nil
	case sourceClone:
		cmd.Starter = ""
		return promptSourceURL(&cmd.Clone, "clone URL", "owner/repo or full git URL")
	case sourceStarter:
		cmd.Clone = ""
		return promptSourceURL(&cmd.Starter, "starter URL", "template repo URL; history is discarded after clone")
	}
	return false, nil
}

func promptSourceURL(target *string, title, desc string) (bool, error) {
	val := *target
	input := huh.NewInput().
		Title(title).
		Description(desc).
		Value(&val).
		Validate(func(s string) error {
			if strings.TrimSpace(s) == "" {
				return errors.New("required")
			}
			return nil
		})
	if err := huh.NewForm(huh.NewGroup(input)).Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return true, nil
		}
		return false, err
	}
	*target = strings.TrimSpace(val)
	return false, nil
}

func promptKartTune(cmd *newCmd, tunes []string) (bool, error) {
	if len(tunes) == 0 {
		// No tunes registered on the circuit: leave it to the server's
		// default. Don't even show an empty picker.
		return false, nil
	}
	const noneSentinel = ""
	opts := make([]huh.Option[string], 0, len(tunes)+1)
	opts = append(opts, huh.NewOption("(server default)", noneSentinel))
	for _, t := range tunes {
		opts = append(opts, huh.NewOption(t, t))
	}
	pick := cmd.Tune
	if pick != "" && !slices.Contains(tunes, pick) {
		pick = noneSentinel
	}
	sel := huh.NewSelect[string]().
		Title("tune").
		Description("Build/workspace/session config (env vars, dotfiles). type to filter.").
		Options(opts...).
		Filtering(true).
		Height(min(12, len(opts)+2)).
		Value(&pick)
	if err := huh.NewForm(huh.NewGroup(sel)).Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return true, nil
		}
		return false, err
	}
	cmd.Tune = pick
	return false, nil
}

func promptKartCharacter(cmd *newCmd, characters []string) (bool, error) {
	if len(characters) == 0 {
		return false, nil
	}
	const noneSentinel = ""
	opts := make([]huh.Option[string], 0, len(characters)+1)
	opts = append(opts, huh.NewOption("(server default)", noneSentinel))
	for _, c := range characters {
		opts = append(opts, huh.NewOption(c, c))
	}
	pick := cmd.Character
	if pick != "" && !slices.Contains(characters, pick) {
		pick = noneSentinel
	}
	sel := huh.NewSelect[string]().
		Title("character").
		Description("Identity (git user + PAT) the kart commits and pushes as. type to filter.").
		Options(opts...).
		Filtering(true).
		Height(min(12, len(opts)+2)).
		Value(&pick)
	if err := huh.NewForm(huh.NewGroup(sel)).Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return true, nil
		}
		return false, err
	}
	cmd.Character = pick
	return false, nil
}

func promptKartAutostart(cmd *newCmd) (bool, error) {
	val := cmd.Autostart
	prompt := huh.NewConfirm().
		Title("autostart on server reboot?").
		Affirmative("yes").
		Negative("no").
		Value(&val)
	if err := huh.NewForm(huh.NewGroup(prompt)).Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return true, nil
		}
		return false, err
	}
	cmd.Autostart = val
	return false, nil
}

// initialSourceMode picks the wizard's starting source selection from
// any flags the user already passed. Default is `none` so the bare
// `drift new` flow lands on the lightest option.
func initialSourceMode(cmd *newCmd) kartSourceMode {
	switch {
	case cmd.Clone != "":
		return sourceClone
	case cmd.Starter != "":
		return sourceStarter
	default:
		return sourceNone
	}
}

// suggestKartName proposes a default name for the input prompt. When
// the user already typed `--clone owner/repo`, the repo segment is the
// obvious default; otherwise leave it blank and let them type one.
func suggestKartName(cmd *newCmd) string {
	for _, src := range []string{cmd.Clone, cmd.Starter} {
		if name := nameFromRepoURL(src); name != "" {
			return name
		}
	}
	return ""
}

// nameFromRepoURL pulls a reasonable kart-name candidate out of a clone
// URL — the trailing `<repo>` segment, lowercased, stripped of `.git`,
// with anything outside the kart-name charset replaced by `-`. Returns
// "" when no usable candidate falls out (server-side validator stays
// the source of truth, so we err on the side of clearing rather than
// guessing).
func nameFromRepoURL(url string) string {
	u := strings.TrimSpace(url)
	if u == "" {
		return ""
	}
	u = strings.TrimSuffix(u, "/")
	u = strings.TrimSuffix(u, ".git")
	if i := strings.LastIndex(u, "/"); i >= 0 {
		u = u[i+1:]
	}
	if i := strings.LastIndex(u, ":"); i >= 0 {
		u = u[i+1:]
	}
	u = strings.ToLower(u)
	var b strings.Builder
	for _, r := range u {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return ""
	}
	if !kartNameRe.MatchString(out) {
		return ""
	}
	return out
}
