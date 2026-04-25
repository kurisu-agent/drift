package drift

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"unicode/utf8"

	"charm.land/huh/v2"
	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/cli/ui"
	"github.com/kurisu-agent/drift/internal/connect"
	driftexec "github.com/kurisu-agent/drift/internal/exec"
	"github.com/kurisu-agent/drift/internal/wire"
	"golang.org/x/term"
)

// skillCmd: `drift skill [name] [prompt]`. With no args on a TTY,
// prints the skill table and drops into a picker + prompt flow; on a
// non-TTY or --output json, prints the table and exits (parity with
// `drift runs`). Name alone prompts for input. Name + prompt is a
// one-shot.
type skillCmd struct {
	Name         string   `arg:"" optional:"" help:"Skill name (see drift skill); omit to list."`
	Prompt       []string `arg:"" optional:"" passthrough:"" help:"Initial prompt forwarded to the skill."`
	SSH          bool     `name:"ssh" help:"Force plain SSH (skip mosh)."`
	ForwardAgent bool     `name:"forward-agent" help:"Enable SSH agent forwarding (-A)."`
}

func runSkillExec(ctx context.Context, io IO, root *CLI, cmd skillCmd, deps deps) int {
	// No name on a non-TTY (or under --output json): render the listing
	// the same way `drift skills`-equivalent would, so scripted callers
	// see a stable, parseable surface instead of hanging on a prompt.
	canPrompt := stdinIsTTY(io.Stdin) && root.Output != "json"
	if cmd.Name == "" && !canPrompt {
		return runSkillList(ctx, io, root, deps)
	}

	_, circuit, err := resolveCircuit(root, deps)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}

	// Picker path: no name (pick from list) or name with no prompt
	// (offer to fill). CLI positional args always bypass the prompt so
	// scripted `drift skill scaffolder "build me X"` is a one-shot.
	if canPrompt && len(cmd.Prompt) == 0 {
		picked, aborted, pErr := pickAndFillSkill(ctx, io, circuit, deps, cmd.Name)
		if pErr != nil {
			return errfmt.Emit(io.Stderr, pErr)
		}
		if aborted {
			return 0
		}
		cmd.Name = picked.name
		cmd.Prompt = []string{picked.prompt}
	}

	prompt := ""
	if len(cmd.Prompt) > 0 {
		prompt = cmd.Prompt[0]
	}

	var res wire.SkillResolveResult
	if err := deps.call(ctx, circuit, wire.MethodSkillResolve, wire.SkillResolveParams{
		Name:   cmd.Name,
		Prompt: prompt,
	}, &res); err != nil {
		return errfmt.Emit(io.Stderr, err)
	}

	useMosh := !cmd.SSH && moshOnPath()
	// res.Command is the server-rendered `cd ~/.drift && … exec claude`
	// stanza; same zellij UX rationale as drift ai.
	bin, argv := buildRunArgv(wire.RunModeInteractive, useMosh, circuit, cmd.ForwardAgent, wrapWithZellij(res.Command))

	p := ui.NewTheme(io.Stderr, root.Output == "json")
	if p.Enabled {
		transport := "ssh"
		if useMosh {
			transport = "mosh"
		}
		fmt.Fprintln(io.Stderr, p.Dim(fmt.Sprintf("→ skill %s (interactive, via %s)", res.Name, transport)))
	}

	stdio := connect.Stdio{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}
	if execErr := driftexec.Interactive(ctx, bin, argv, stdio.Stdin, stdio.Stdout, stdio.Stderr); execErr != nil {
		var ee *driftexec.Error
		if errors.As(execErr, &ee) && ee.ExitCode != 0 {
			return ee.ExitCode
		}
		return errfmt.Emit(io.Stderr, execErr)
	}

	if hookErr := runPostHook(ctx, io, root, deps, circuit, res.Post); hookErr != nil {
		return errfmt.Emit(io.Stderr, hookErr)
	}
	return 0
}

// runSkillList is the bare `drift skill` path: render the catalog, then on
// a TTY drop into the picker + prompt + run flow. Non-TTY callers get the
// same output as `drift skills` (table or JSON) and exit cleanly.
func runSkillList(ctx context.Context, io IO, root *CLI, deps deps) int {
	_, circuit, err := resolveCircuit(root, deps)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	var list wire.SkillListResult
	if err := deps.call(ctx, circuit, wire.MethodSkillList, struct{}{}, &list); err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	if rc := renderSkillsOutput(io, root, list); rc != 0 {
		return rc
	}

	// Non-TTY / JSON / empty-registry callers stop at the listing; TTY
	// text callers fall through into the full pick-prompt-run flow below.
	if root.Output == "json" || !stdinIsTTY(io.Stdin) || len(list.Skills) == 0 {
		return 0
	}
	return skillInteractiveAfterList(ctx, io, root, deps, circuit, list)
}

// renderSkillsOutput prints the skill roster as a table (or JSON) and
// returns. Shared between `drift skills` (plural, print-only) and bare
// `drift skill` on a non-TTY so the two paths can't drift apart.
func renderSkillsOutput(io IO, root *CLI, list wire.SkillListResult) int {
	if root.Output == "json" {
		buf, mErr := json.MarshalIndent(list, "", "  ")
		if mErr != nil {
			return errfmt.Emit(io.Stderr, mErr)
		}
		fmt.Fprintln(io.Stdout, string(buf))
		return 0
	}
	if len(list.Skills) == 0 {
		fmt.Fprintln(io.Stdout, "no skills on this circuit")
		fmt.Fprintln(io.Stdout, "  drop SKILL.md files into ~/.claude/skills/<name>/ on the circuit")
		return 0
	}
	p := ui.NewTheme(io.Stdout, false)
	rows := make([][]string, 0, len(list.Skills))
	for _, s := range list.Skills {
		rows = append(rows, []string{s.Name, s.Description})
	}
	writeTable(io.Stdout, p, []string{"NAME", "DESCRIPTION"}, rows, accentCellStyler(0))
	return 0
}

// skillInteractiveAfterList is the tail of the TTY listing path: pick
// a skill from the freshly-printed table, collect a prompt, run it.
// Separated so runSkillList can cleanly short-circuit on non-TTYs.
func skillInteractiveAfterList(ctx context.Context, io IO, root *CLI, deps deps, circuit string, list wire.SkillListResult) int {
	if len(list.Skills) == 0 {
		return 0
	}
	picked, aborted, err := pickSkillFromList(list.Skills)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	if aborted {
		return 0
	}
	prompt, aborted, err := promptForSkillInput(picked.Name)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	if aborted {
		return 0
	}
	return runSkillExec(ctx, io, root, skillCmd{Name: picked.Name, Prompt: []string{prompt}}, deps)
}

// pickedSkill pairs a chosen skill with whatever initial prompt the
// user typed at the follow-up widget.
type pickedSkill struct {
	name   string
	prompt string
}

// pickAndFillSkill drives the interactive picker. Mirrors pickAndFillRun
// but flattens the per-arg loop to a single "Prompt" widget, since
// skills don't declare arg shapes.
func pickAndFillSkill(ctx context.Context, io IO, circuit string, deps deps, nameHint string) (*pickedSkill, bool, error) {
	var list wire.SkillListResult
	if err := deps.call(ctx, circuit, wire.MethodSkillList, struct{}{}, &list); err != nil {
		return nil, false, err
	}
	if len(list.Skills) == 0 && nameHint == "" {
		fmt.Fprintln(io.Stdout, "no skills on this circuit")
		fmt.Fprintln(io.Stdout, "  drop SKILL.md files into ~/.claude/skills/<name>/ on the circuit")
		return nil, true, nil
	}

	var chosen *wire.Skill
	if nameHint == "" {
		picked, aborted, err := pickSkillFromList(list.Skills)
		if err != nil || aborted {
			return nil, aborted, err
		}
		chosen = picked
	} else {
		for i := range list.Skills {
			if list.Skills[i].Name == nameHint {
				chosen = &list.Skills[i]
				break
			}
		}
		if chosen == nil {
			// Unknown name: hand back to skill.resolve so the server
			// produces the canonical not-found error (shape + type
			// preserved).
			return &pickedSkill{name: nameHint}, false, nil
		}
	}
	prompt, aborted, err := promptForSkillInput(chosen.Name)
	if err != nil || aborted {
		return nil, aborted, err
	}
	return &pickedSkill{name: chosen.Name, prompt: prompt}, false, nil
}

// pickSkillFromList renders a filterable huh.Select. Labels pair name
// and description for readability, matching pickRunEntry's shape.
func pickSkillFromList(skills []wire.Skill) (*wire.Skill, bool, error) {
	// Skill descriptions are written for the Claude SKILL.md harness and
	// run multi-paragraph; left raw, every option wraps to 4-6 lines and
	// the picker becomes unscannable. Truncate to the visible width so
	// each option stays a single row.
	descBudget := skillDescBudget()
	opts := make([]huh.Option[string], 0, len(skills))
	for _, s := range skills {
		label := s.Name
		if s.Description != "" {
			label = fmt.Sprintf("%-20s — %s", s.Name, truncateRunes(s.Description, descBudget))
		}
		opts = append(opts, huh.NewOption(label, s.Name))
	}
	var pick string
	sel := huh.NewSelect[string]().
		Title("drift skill").
		Description("Pick a skill · type to filter · enter to continue · esc/ctrl+c to quit").
		Options(opts...).
		Filtering(true).
		Height(18).
		Value(&pick)
	if err := huh.NewForm(huh.NewGroup(sel)).Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return nil, true, nil
		}
		return nil, false, err
	}
	for i := range skills {
		if skills[i].Name == pick {
			return &skills[i], false, nil
		}
	}
	return nil, true, nil
}

// skillDescBudget returns how many runes of description fit on one row
// of the picker, accounting for the huh selector prefix ("> "), the
// 20-char name padding, and the " — " separator. Falls back to 80 cols
// when stdout's width is unknown (pipe, non-TTY).
func skillDescBudget() int {
	width := 80
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 { //nolint:gosec // G115: posix file descriptors always fit in int
		width = w
	}
	// "> " (2) + name pad (20) + " — " (3) + small slack for huh's frame.
	const overhead = 30
	budget := width - overhead
	if budget < 20 {
		budget = 20
	}
	return budget
}

// truncateRunes shortens s to at most n runes, appending an ellipsis
// when it had to cut. Rune-aware so the em-dash and other multi-byte
// glyphs in skill descriptions don't get sliced mid-codepoint.
func truncateRunes(s string, n int) string {
	if n <= 0 || utf8.RuneCountInString(s) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	runes := []rune(s)
	return string(runes[:n-1]) + "…"
}

// promptForSkillInput collects the user's initial message. Multi-line
// text widget because scaffolder-style skills want paragraph prompts,
// not one-liners; single-line users can still hit enter when done.
func promptForSkillInput(skillName string) (string, bool, error) {
	title := "Prompt for " + skillName
	var val string
	field := huh.NewText().Title(title).Value(&val)
	if err := huh.NewForm(huh.NewGroup(field)).Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return "", true, nil
		}
		return "", false, err
	}
	return val, false, nil
}
