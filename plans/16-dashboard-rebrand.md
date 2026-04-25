# Plan 16 — drift dashboard rebrand and visual polish

## Problem

Plan 14 landed the foundation: the `internal/cli/ui` package, mode/theme/surface, the eight-tab dashboard skeleton, harmonica entrance animation, snapshot tests, demo mode, and `make eval-frames`. It works end-to-end. It does not look the way drift should look. The current eight panels are bare: weak hierarchy, no panel borders, low-contrast text, an afterthought of a banner, no visible focus accents, no badges, no toasts, no per-circuit color, no Nerd Font icons (the catalog is BMP-only with an ASCII fallback). The plan-14 research doc (`plans/14-research.md`) cataloged the patterns shipping apps in this ecosystem use to feel polished. None of those patterns is wired in yet.

Plan 16 supersedes the dashboard parts of plan 14. The foundation work is done. We commit to one polished look, ground the implementation against per-page screenshot rubrics, and grind toward the rubric over a long-running iterative loop. The deliverable is the dashboard at a quality bar matching the high-polish projects surveyed in the research doc (Crush, gh-dash, soft-serve, fleetd) while staying within the constraints drift cares about (Termux first-class, ssh-served acceptable, scrollback-tolerant).

This plan also serves as drift's brand guidelines document. Future agents working on any drift visual surface should copy from the "Brand guidelines" section below.

## Tying up plan 14

Plan 14 stays merged. Its foundation pieces are not getting reverted. What changes is the active work scope: the unfinished items in `plans/14-progress.md` are re-bucketed into:

- **Folded into plan 16** (because they are chrome decisions tied up in the rebrand, not features): cross-tab affordances (`:` command palette, `?` full-help modal, toast region), per-panel filter/sort/expand UX, focus-pause on terminal blur, tab strip styling, footer styling, snapshot harness updates.
- **Deferred to follow-up plans** (they are features that benefit from the rebrand but do not block it): karts panel lifecycle action wiring with confirmation modals + RPC dispatch, circuits add/delete/rename, ports panel data wiring, logs panel viewport + follow + filter, `drift new` huh wizard, glamour-rendered `drift help <topic>`, drive-by per-command migrations to `ui.Confirm` / `ui.Select` / `ui.Input` wrappers, replacing `drift update`'s `\r`-rewrite progress writer.
- **Closed out** (still relevant but no longer "in progress"): theme adaptive selection + colorprofile downsampling, Mode/Surface contracts, harmonica entrance, demo mode, snapshot regression gate.

`14-progress.md` gets a closing paragraph pointing at this plan; its "still to do" list is moved here in re-scoped form. The `feat/plan-14-fresh` branch continues to be the working branch. No new worktree.

## Goals

1. **One polished look, end-to-end across eight tabs.** Every tab reads as part of the same product. Borders, focus accents, type hierarchy, icons, and motion are consistent. No tab feels like an unfinished prototype next to the others.
2. **Nerd Font assumed.** No more `DRIFT_NO_NERDFONT` opt-out, no more parallel ASCII fallback table. The icon catalog is rewritten against Nerd Font code points. Terminals without a Nerd Font render tofu; that is acceptable.
3. **Per-page screenshot rubric is the regression gate.** The agent doing this work generates frames via `make eval-screens`, reads them alongside `docs/eval/rubric.md`, and iterates until each rubric passes. Snapshot tests catch byte-level drift; the rubric catches visual drift the snapshots cannot see.
4. **Brand guidelines doc.** This plan's "Brand guidelines" section is copy-pasteable into agent prompts and review comments. Future visual work should cite it.
5. **Ground for the long-running loop.** This plan is structured so an agent can pick up any tab, work it to rubric-clean, commit, move on. No tab depends on another being finished first.

## Non-goals

- **Not feature work.** Lifecycle action wiring, RPC dispatch, modals that mutate server state, the `drift new` wizard, the ports panel data wiring: not in scope for this plan. They land in follow-up plans built on the rebranded surface.
- **Not multi-platform validation.** We assume Ghostty / kitty / iTerm2 / Termux with a Nerd Font installed. Other terminals are best-effort. Windows-native console quirks are out.
- **Not a CI-gated visual loop.** The screenshot rubric is a developer-loop tool, not a build gate. Snapshot tests + lint + race tests stay the CI gate.
- **Not new dependencies.** We have everything: bubbletea v2, lipgloss v2, bubbles v2, huh v2, harmonica, charmbracelet/log, freeze (via Makefile shell-out). No additions.

## Brand guidelines

Don't redefine what the framework gives us. Within the Charm v2 stack's defaults, drift commits to these specific picks. Anything not listed here, take the framework default.

**Theme.** Use `huh.ThemeCharm()` for forms; for the dashboard, build the theme from `charmbracelet/x/exp/charmtone` named tokens (Charple, Pepper, Iron, Squid, etc.) rather than hand-rolled hex codes. Light/dark via `lipgloss.LightDark(isDark)` driven once by `tea.BackgroundColorMsg`. The only place we override the framework default: pick a single brand accent (`charmtone.Charple` for now, swap if Charm renames it) and use it consistently for focus + active tab.

**Borders.** `lipgloss.RoundedBorder()` everywhere. Active vs blurred is color, never bg or border-style swap. Geometry stays stable on focus.

**Tab strip.** Adopt `bubbletea/examples/tabs/main.go` welded-border pattern verbatim. Don't customize it.

**Footer.** `bubbles/help` short mode by default, full on `?`. Don't restyle the separator or key glyphs; framework defaults are fine. Disabled bindings via `key.SetEnabled(false)` so `help` filters them.

**Status indication.** Inline icon + colored label for ambient status (activity rows, stats); status pill (`Bold(true).Padding(0,1)` with status-color bg) only when scanning a column of states matters (karts table status column, ports conflict column).

**Toasts.** Bottom-right, 3s TTL via `list.NewStatusMessage`-shaped `tea.Cmd`. No fade animation; appear and disappear.

**Modals.** `lipgloss.Place(Center, Center)` over the body. RoundedBorder with focus color. One file per dialog (Crush pattern).

**Icons.** Nerd Font assumed. `internal/cli/ui/icons.go` rewritten against `nf-md-*` (Material Design) and `nf-fa-*` (Font Awesome) glyphs. The `DRIFT_NO_NERDFONT` fallback is removed. Pick a code point per semantic name; the catalog lives in `icons.go`. Keep the catalog small; add icons as panels need them, don't preload speculatively.

**Motion.** Existing 600ms harmonica entrance on the status panel stays. No motion on tab switch, refresh, or panel repaint. `bubbles/spinner` with `spinner.Dot` for indeterminate.

**Per-circuit color tint.** Optional `color: "#hex"` on the circuit config (workstation-side). When the dashboard is anchored to one circuit, the brand accent (focus border, active tab) derives from it; cross-circuit views revert to the default brand accent. Single most important safety affordance for multi-environment users (research doc, ktea pattern).

**Voice.** Lowercase, terse, no exclamation. Empty states are one short sentence ("no karts yet, drift new to create one"). Errors name the failure in user terms.

That's the brand guidelines. Future agents copy this section as-is into prompts; everything else is "use the framework default."

## Screenshot verification loop

### What we have already

`cmd/dashboard-frame` builds a single ANSI frame for `-tab <name> -w <cols> -h <rows>` against demo fixtures. `make eval-frames` iterates the eight tabs at 120×30 and writes `docs/eval/<tab>.png` via `freeze`. Both exist and work.

### What plan 16 adds

1. **Multi-scenario capture per tab.** Extend `cmd/dashboard-frame` to accept `-scenario <name>` selecting an in-fixtures world state plus pre-driven key sequence. New target: `make eval-screens`. Scenarios per tab:

```
status:      default
karts:       default · filter-active · row-expanded
circuits:    default · with-color-tint
chest:       default · row-expanded
characters:  default · row-expanded
tunes:       default · row-expanded
ports:       default · with-conflict
logs:        default · follow-on · filter-active
cross-cut:   palette-open · help-modal · toast-success · toast-error · narrow-80c
```

Output naming: `docs/eval/<tab>-<scenario>.png`. Default is the only scenario for a tab unless an alternate is listed.

2. **`docs/eval/rubric.md` (new).** Markdown checklist with cross-cutting questions and per-page sections. The agent reads this file alongside the PNGs; both go into the prompt.

3. **`make eval-loop` (new helper target).** Captures frames and prints the rubric path so the agent can `Read docs/eval/rubric.md` plus `Read docs/eval/*.png` in one go. Pure convenience.

### Rubric structure

`docs/eval/rubric.md` opens with cross-cutting questions, then has one section per scenario referencing its PNG. Cross-cutting list (initial):

```
- Wordmark renders with even rainbow gradient across all letterforms.
- Outer dashboard border is visible and rounded; weight is theme.Border.Subtle.
- Active tab is welded into the body via the canonical border-cut pattern.
- Active tab label uses theme.Border.Focus color; inactive labels use theme.Text.Muted.
- Tab separator dots are visible and not over-emphasized.
- Footer help line uses the IconKey prefix and middle-dot separators.
- Status pills (where present) read at a glance: icon + one word + status bg.
- No tab feels visibly less finished than the others.
- No raw ANSI escape sequences leak into rendered text.
- Nerd Font icons render as glyphs, not tofu (verify font is installed).
- Right-aligned columns are actually right-aligned.
- Dim / muted / regular text levels are visually distinguishable.
- Borders, padding, and panel chrome are consistent across panels.
- One element per panel draws the eye first; the page has visual hierarchy.
```

Per-page sections add panel-specific checks (e.g. status panel: stats block alignment, banner balance, activity table column rhythm, lockup vertical alignment with banner). The rubric is a living document; update it as new patterns emerge.

### The loop

```
1. Edit code (one panel or one cross-cutting concern at a time).
2. make eval-screens
3. In a Claude Code session, prompt: "Read docs/eval/rubric.md and all of
   docs/eval/*.png. Evaluate each frame against the rubric. List failures
   with severity and suggested fixes."
4. Address failures in order of severity. Repeat from step 2.
5. When the rubric is clean for the panel, commit. Move to the next.
```

The snapshot tests (`internal/cli/ui/dashboard/snapshot_test.go`) catch byte-level regressions; rebaseline with `go test ./internal/cli/ui/dashboard/... -update` after intentional UI changes. The rubric catches visual regressions snapshots cannot see.

## Per-tab redesign targets

Each subsection lists rubric-checked applications of the brand guidelines. Sketches are shape-only; the implementation follows lipgloss / bubbles idioms when they conflict with the sketch.

### status

The flagship tab. Banner + stats block in the top row; activity table below. Apply:

- Banner block: existing harmonica entrance; refine gradient stops to use `gradient-rainbow` exactly. Lockup alignment: top of "drift {version}" aligns with row 1 of the wordmark; bottom tagline aligns with row 3.
- Stats block: right-aligned, three lines, format `N / M label` with N bold in `theme.Text.Primary`, M in `theme.Text.Muted`, label in `theme.Text.Subtle`. Inline icons before label: `IconCircuit` / `IconKart` / `IconPort`.
- Activity table: TIME column right-aligned in `theme.Text.Subtle`; ACTION column with inline status icon + label; KART column showing `<circuit>.<kart>` with `IconBullet` between or `—` for global; DETAIL in `theme.Text.Muted`. Row spacing 0.
- Refresh: 10s ticker for stats; activity is event-driven.

### karts

Cross-circuit kart table. Apply:

- Status pill column (`theme.Pill`): `running` mint bg / `stopped` muted / `stale` amber / `error` coral. Pill is leftmost column.
- Name column: `<circuit>.<kart>` with circuit half in `theme.Text.Subtle`, kart half in `theme.Text.Primary`.
- Image column: `theme.Text.Muted`, monospaced.
- Last-active column: relative time in `theme.Text.Subtle`.
- Selected row: left bar `theme.Border.Focus`, no bg change.
- Filter (`/`): inline footer prompt; non-matching rows dim via `StyleLineFunc`, do not collapse (per research doc, wander pattern).
- Empty state: centered, `theme.Text.Muted`, single sentence.

### circuits

Circuit-level admin. Apply:

- Per-circuit color tint visible in a left-bar gutter on each row, drawn from circuit `color` config.
- Default-circuit row prefixed with `IconStar` in `theme.Accent.Primary`.
- Reachability column with `IconRunning` / `IconUnreachable` and label.
- `space` to set default, `enter` to drill into per-circuit detail, `a` to add (inline), `d` to delete (confirm modal).

### chest

Read-only env-refs. Apply:

- Inline note at panel top: "authoring stays in lakitu chest …" in `theme.Text.Subtle` italic.
- Table: name (primary), backend (muted), last-used (subtle), used-by-karts (muted with count badge).
- Row expand on `enter`: backend resolver detail block plus cross-link to kart references in `theme.Text.Muted`.

### characters

Same shape as chest. Apply:

- Inline note: "authoring stays in lakitu character edit".
- Table: name, git name, git email, PAT ref, kart count.
- Row expand: container username, dotfiles, kart references.

### tunes

Same shape as chest. Apply:

- Inline note: "authoring stays in lakitu tune edit".
- Table: name, base image, feature count, used-by-karts.
- Row expand: full devcontainer fragment in a viewport, syntax-styled (lipgloss multi-line render, no glamour for now).

### ports

Active forwards + remaps. Apply:

- Direction column with `IconConnect` arrow direction explicit (`:3000` dim → `:3000` primary).
- Conflict state in coral with `IconError` prefix when host port collides.
- Add (`a`) / delete (`d`) inline form at footer.
- (Data wiring stays out of plan 16; the panel renders demo fixtures only.)

### logs

Kart picker on top, scrollable viewport below. Apply:

- Kart picker: compact one-line list with selected row highlighted via left bar.
- Viewport: `bubbles/v2/viewport` with `LeftGutterFunc` rendering timestamp `15:04:05` in `theme.Text.Subtle` followed by level pill.
- Level badge column: `INFO` / `WARN` / `ERROR` rendered as small pills in `theme.Status.*` bg.
- Follow indicator: `IconRunning` in `theme.Accent.Primary` next to "follow" in the panel title when on; otherwise dim.
- Filter (`/`): inline; non-matching lines dim via `StyleLineFunc`.
- (Real data wiring deferred; panel renders fixture log content only.)

### Cross-cut: command palette and help modal

Both are dialogs from the dialog stack. Apply:

- Command palette (`:` global): textinput at top in primary, filterable list below grouped by category (System / Karts / Ports / Tunes / Skills). Per-row icon + label + dim shortcut hint. Press enter to dispatch.
- Help modal (`?`): two-column layout, left = global keys, right = active-tab keys. Same dialog chrome.

## Long-running agent flow

This plan is designed to be ground through over many sessions. The intended workflow:

1. The agent picks a panel from the per-tab list above (or a cross-cutting concern).
2. The agent reads `plans/14-research.md` (research) and `plans/16-dashboard-rebrand.md` (this plan, especially the brand-guidelines section).
3. The agent edits the relevant files. Branch stays `feat/plan-14-fresh`.
4. The agent runs `make eval-screens`, then in the same session does `Read docs/eval/rubric.md` plus `Read docs/eval/*.png` (or just the panel's frames).
5. The agent lists failures, addresses them, regenerates frames, repeats until rubric-clean.
6. The agent runs `go test ./internal/cli/ui/...` and rebaselines snapshots with `-update` if changes were intentional.
7. Commit on `feat/plan-14-fresh`; commit shape is up to the agent. Move to the next panel.

The agent should not block panels on each other. The brand guidelines are the contract; if two panels look subtly different, both adjust toward the guidelines, not toward each other.

## Architecture notes

### Theme construction

`internal/cli/ui/theme.go` grows into a small package: `theme/tokens.go` (palette), `theme/groupings.go` (semantic), `theme/components.go` (per-component sub-structs). One `New(isDark bool, profile colorprofile.Profile, circuitColor *color.Color)` constructor materializes the whole tree. Caller pattern:

```go
t := theme.New(bg.IsDark, prof, currentCircuit.Color)
panel := t.Panel.Border.Render(...)
```

### Icon registry rewrite

`internal/cli/ui/icons.go` switches all `rune` constants to Nerd Font code points. The `Icon()` helper drops its `DRIFT_NO_NERDFONT` branch. Hair-space wrapping (`Icon(IconRunning) + " " + label`) becomes the convention; consider a small `IconLabel(IconRunning, "running")` helper that handles the spacing once.

### Dialog stack

`internal/cli/ui/dashboard/dialog/` (new package). `Overlay` struct holds a `[]Dialog` stack; `Update(msg)` routes only to front; `View` renders with `lipgloss.Place` centering. One file per dialog: `confirm.go`, `command_palette.go`, `help_modal.go`.

### Toast system

`internal/cli/ui/dashboard/toast.go` (new). `Toast{Level, Message, Context, BornAt}`. Active toasts live on the dashboard model; `View` overlays them bottom-right via lipgloss layer composition. TTL handled by `tea.Tick`.

### Per-circuit color

`internal/lakitu/circuit.go` (or wherever circuit config is read) gains an optional `Color string` field. The dashboard model holds `currentCircuitColor *color.Color`; the theme accepts it and overrides `Border.Focus` when set.

## Testing

Snapshot tests stay the byte-level regression gate (`internal/cli/ui/dashboard/snapshot_test.go`). Per intentional UI change, rebaseline with `-update`. Add scenarios for the multi-scenario screenshots (filter active, row expanded, palette open) so byte-level drift is caught early.

`teatest`-style scripted-flow tests for the dashboard happy paths land alongside but are not strictly required to advance through plan 16. They are the right next step after the rebrand stabilizes.

`make ci` (`tidy vet test-race lint vuln`) stays green on every commit.

## Delivery

Plan 16 lands as continued work on `feat/plan-14-fresh` inside the existing plan-14 PR. No branch split, no per-panel PRs. Commit shape inside the branch is up to whoever's working; don't optimize for that.

Rough work order, not strict:

1. Rubric file (`docs/eval/rubric.md`).
2. Theme rewrite against charmtone tokens.
3. Icon catalog rewrite (Nerd Font, drop fallback).
4. Outer dashboard chrome and `make eval-screens` extension.
5. Per-tab polish: status, karts, circuits + per-circuit color tint, chest / characters / tunes, ports, logs.
6. Cross-cut: command palette, help modal, toasts.
7. Final rubric pass; rebaseline snapshots.

## Open questions

- **Per-circuit color storage location.** Workstation `~/.config/drift/circuits.yaml` is the obvious answer (per the client/server boundary rule, circuits are workstation-defined). Confirm this aligns with the existing circuit config shape.
- **Toast region position.** Bottom-right is the plan; alternative is footer-replacement (one line, replaces help temporarily). Pick during implementation; bottom-right is more visible but eats screen real estate.
- **Sidebar tabs as a fallback at narrow widths.** Below ~80 cols the welded tab strip wraps. Plan-14's solution is numeric pips. Worth seeing puffin's left-sidebar layout in real terminal width once implementation starts; if pips read poorly we have a backup.
- **Glyph code points need verification.** The Nerd Font v3.x catalog above is correct as of this writing but glyph code points have shifted historically. The first agent to work the icon rewrite should verify each one renders as expected before merging.
- **Should the brand guidelines extract to `docs/brand.md`?** Right now they live in this plan. Once the rebrand is mostly done, lifting them into a standalone doc makes them easier to link to from agent prompts. Defer until plan 16 stabilizes.
