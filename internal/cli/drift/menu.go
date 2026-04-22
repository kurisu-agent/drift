package drift

import (
	"errors"

	"github.com/charmbracelet/huh"
)

// menuEntry describes a single row in the top-level interactive menu.
// When `needs` is empty the command runs immediately with `argv`; otherwise
// the user is prompted for a single value that is appended to `prefix`.
type menuEntry struct {
	key    string
	argv   []string
	needs  string
	prefix []string
}

var menuEntries = []menuEntry{
	{key: "setup › status  — circuits + lakitu health + kart counts", argv: []string{"status"}},
	{key: "setup › init    — interactive first-time setup wizard", argv: []string{"init"}},
	{key: "setup › update  — check GitHub for a newer drift release", argv: []string{"update"}},
	{key: "setup › help    — LLM-friendly command + protocol reference", argv: []string{"help"}},

	{key: "circuit › list", argv: []string{"circuit", "list"}},
	{key: "circuit › add           — register a new circuit (user@host)", needs: "user@host", prefix: []string{"circuit", "add"}},
	{key: "circuit › rm            — remove a circuit by name", needs: "circuit name", prefix: []string{"circuit", "rm"}},
	{key: "circuit › set default   — make a circuit the default", needs: "circuit name", prefix: []string{"circuit", "set", "default"}},
	{key: "circuit › set name      — rename the active circuit", needs: "new circuit name", prefix: []string{"circuit", "set", "name"}},

	{key: "kart › list", argv: []string{"list"}},
	{key: "kart › new       — create a new kart", needs: "kart name", prefix: []string{"new"}},
	{key: "kart › info      — show a kart's state", needs: "kart name", prefix: []string{"info"}},
	{key: "kart › start", needs: "kart name", prefix: []string{"start"}},
	{key: "kart › stop", needs: "kart name", prefix: []string{"stop"}},
	{key: "kart › restart", needs: "kart name", prefix: []string{"restart"}},
	{key: "kart › delete", needs: "kart name", prefix: []string{"delete"}},
	{key: "kart › enable    — autostart on reboot", needs: "kart name", prefix: []string{"enable"}},
	{key: "kart › disable", needs: "kart name", prefix: []string{"disable"}},
	{key: "kart › logs", needs: "kart name", prefix: []string{"logs"}},
	{key: "kart › connect   — mosh/ssh into a kart", argv: []string{"connect"}},

	{key: "run › list       — server-side shorthand commands", argv: []string{"runs"}},
	{key: "run › ai         — launch claude on the circuit", argv: []string{"run", "ai"}},
	{key: "run › scaffolder — AI-scaffold a new project + kart", argv: []string{"run", "scaffolder"}},
}

// runMenu presents the top-level picker. Returns the argv that should be
// dispatched through normal Kong parsing. A nil return with nil error
// signals the user cancelled (e.g. ctrl+c) — callers should treat that as
// a clean exit.
func runMenu(io IO) ([]string, error) {
	var pick string
	opts := make([]huh.Option[string], 0, len(menuEntries))
	for _, e := range menuEntries {
		opts = append(opts, huh.NewOption(e.key, e.key))
	}

	sel := huh.NewSelect[string]().
		Title("drift").
		Description("Pick a command · type to filter · enter to run · esc/ctrl+c to quit").
		Options(opts...).
		Filtering(true).
		Height(18).
		Value(&pick)

	if err := huh.NewForm(huh.NewGroup(sel)).Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return nil, nil
		}
		return nil, err
	}

	var entry *menuEntry
	for i := range menuEntries {
		if menuEntries[i].key == pick {
			entry = &menuEntries[i]
			break
		}
	}
	if entry == nil {
		return nil, nil
	}
	if entry.needs == "" {
		return entry.argv, nil
	}

	var val string
	input := huh.NewInput().
		Title(entry.needs).
		Value(&val).
		Validate(func(s string) error {
			if s == "" {
				return errors.New("required")
			}
			return nil
		})
	if err := huh.NewForm(huh.NewGroup(input)).Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return nil, nil
		}
		return nil, err
	}

	argv := append([]string{}, entry.prefix...)
	argv = append(argv, val)
	return argv, nil
}
