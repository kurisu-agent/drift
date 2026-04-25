# drift TUI redesign — bubbletea-first presentation layer

## Problem

Drift's CLI presentation grew organically. Each subcommand reaches for what was nearby at the time: `briandowns/spinner` for long-runs, `mattn/go-isatty` + a hand-rolled `style.Palette` for color, ad-hoc `lipgloss/table` for tables, `huh.Select` for pickers, `text/tabwriter` as a non-TTY fallback, raw `fmt.Fprintln` everywhere else. The result works but is inconsistent: every command renders its own header shape, error indents differ between `errfmt` and inline `fmt.Errorf` paths, the spinner library doesn't share a palette with `lipgloss`, and there is no place to put richer interactive surfaces (multi-pane status, live log tails, port management) when they arrive. With plan 13 (`drift ports`) shipping as a plain-text/JSON CLI and a `drift dashboard` TUI on the way, the right move is to land the shared presentation layer first so every interactive surface is built on the same foundation. Without it, every new TUI re-invents key bindings, theming, and TTY-fallback discipline.

This plan unifies the presentation layer on the Charm v2 ecosystem (`charm.land/bubbletea/v2`, `bubbles/v2`, `lipgloss/v2`, `huh/v2`, optional `glamour/v2`), retires the bespoke spinner/style packages where bubbles equivalents are stronger, and lands a flagship `drift dashboard` TUI with tabs as the showcase surface.

## Goals

1. **One presentation package.** All user-facing rendering — colors, tables, spinners, progress bars, prompts, headers, errors — flows through a single `internal/cli/ui` package built on lipgloss v2 + bubbles v2. Subcommand code asks for "the success line", "the kart table", "the connect picker"; it never reaches for raw lipgloss or bubbletea directly.
2. **TTY/non-TTY parity.** Every surface has both a bubbletea path (when stdout is a TTY and `--no-tui` / `--output json` are not set) and a deterministic plain-text path for pipes, CI, and scripted callers. The plain path is line-buffered, ANSI-free under `NO_COLOR`, and stable across releases. We never rely on bubbletea's nil renderer to fake non-interactive output.
3. **Adaptive theming.** One palette, computed once at startup from `lipgloss.HasDarkBackground` + `LightDark`, with explicit overrides for Termux (often light, often 256-color, sometimes 16-color) and dumb terminals. `colorprofile` downsamples; `NO_COLOR` strips entirely.
4. **Self-documenting key bindings.** Every interactive surface declares its keys as `bubbles/key.Binding` with help text and renders its footer via `bubbles/help`. No hand-typed `[q] quit  [/] filter` strings.
5. **A `drift dashboard` TUI.** A new long-lived bubbletea program that gives a tabbed live view of every circuit, kart, and port forward in one place. Replaces the "open three terminals to run `drift status`, `drift karts`, `drift ports list` and tail logs" workflow.
6. **Standardize the CLI surface where it helps.** Subcommands, flags, and output shapes are not frozen — drift is pre-1.0, and the existing CLAUDE.md backwards-compatibility rule explicitly applies here. If renaming a subcommand or restructuring its flags makes the command read more like the framework's idioms (or like the rest of the redesigned surface), do it.

## Non-goals

- **Removing `--output json`.** Every command that emits JSON today keeps emitting JSON, and the flag stays on the CLI surface — scripted callers always have a deterministic, ANSI-free path. Per the standardization goal above, the *shape* of the JSON can change when it improves consistency (renamed fields, new fields, restructured nesting); call those out in release notes. What's non-negotiable is that JSON output exists and is parseable.
- **A driftd / long-lived background process.** The dashboard is a foreground program the user runs and quits. State for live views comes from existing RPC + `~/.config/drift/ports.yaml` (plan 13). No new daemons.
- **Markdown help everywhere.** `glamour` enters only for the long-form `drift help <topic>` surface (later, optional). Short `--help` stays plain so it's grep-friendly.
- **Mouse-driven interaction.** Keyboard-first across the board. Mouse may be enabled in the dashboard for scrolling but not load-bearing.
- **Windows-native polish.** Drift targets Linux/macOS/Termux. WSL is fine; native Windows console quirks are not in scope.
- **Custom-painted graphics.** No box-drawing animations beyond what lipgloss/bubbles ship. We're polishing a CLI, not building a roguelike.

## Architecture

### Package layout

```
internal/cli/ui/                    new — the unified presentation layer
  theme.go                          palette, light/dark selection, color profile detection
  keys.go                           shared key.Binding declarations (quit, help, filter, refresh, ...)
  header.go                         page-title block ("drift status — circuit alpha")
  table.go                          replaces internal/cli/drift/table.go
  status.go                         success / warning / failure single-line renderers
  spinner.go                        replaces internal/cli/progress (briandowns/spinner → bubbles/spinner)
  progress.go                       progress bars (bubbles/progress) + multi-phase tracker
  prompt.go                         huh wrappers: confirm, select, input
  picker.go                         the cross-circuit-kart picker, factored out
  viewport.go                       scrollable text region (bubbles/viewport) for log tails
  tea.go                            tea.Program helpers: WithTTYFallback, signal/ctx wiring
  testkit.go                        teatest helpers + non-TUI snapshot helpers

internal/cli/ui/dashboard/          new — flagship TUI
  model.go                          root model: tab state, focused panel, refresh ticker
  tabs.go                           tab bar (status / karts / circuits / ports / logs)
  panels/
    status.go                       circuit roster with live latency
    karts.go                        cross-circuit kart table, filterable
    circuits.go                     circuit-level detail + add/remove
    ports.go                        plan-13 ports view, embedded
    logs.go                         tail any kart's logs
  keymap.go
  theme.go                          dashboard-specific style overrides
```

`internal/cli/style` and `internal/cli/progress` are removed once their callers move to `internal/cli/ui`. `internal/cli/errfmt` stays — its job is error → exit-code mapping plus indented data blocks; the new package consumes its rendered string and wraps it for both plain and TUI surfaces.

### TTY detection and the fallback contract

Every command entrypoint computes one boolean at the top:

```go
mode := ui.DetectMode(cmd.Stdout, cmd.Stderr, ui.ModeFlags{
    JSON:    flags.OutputJSON,
    NoTUI:   flags.NoTUI || os.Getenv("DRIFT_NO_TUI") != "",
    NoColor: os.Getenv("NO_COLOR") != "",
    Debug:   flags.Debug,
})
```

`mode` is one of:

- `ModeJSON` — emit JSON to stdout, errors via errfmt to stderr, no spinner, no color, no model.
- `ModePlain` — line-based stdout, no ANSI, no spinner. Pipes, CI, `NO_COLOR`, non-TTY.
- `ModeColor` — stdout is a TTY, color on, spinner on, **no bubbletea program**. This is the default for short commands (`drift list`, `drift status`, `drift new`'s spinner). It's also the safe fallback when the user passes `--no-tui`.
- `ModeTUI` — stdout is a TTY, color on, full bubbletea program with alt-screen. Used by `drift dashboard`, `drift menu`, `drift connect`'s picker, `drift migrate`'s wizard.

The contract: every renderer in `internal/cli/ui` accepts a `*ui.Surface` (carrying mode + theme + writers) and degrades automatically. `surface.Table(headers, rows)` renders a lipgloss table in `ModeColor`/`ModeTUI`, a tabwriter table in `ModePlain`, and is a no-op in `ModeJSON` (the caller emits JSON instead). `surface.Spinner(msg)` is a real animated spinner in TUI/Color, a single "doing X..." line in Plain, silent in JSON.

This is the same shape `internal/cli/style.For` has today — the change is widening it to cover spinners/tables/prompts/progress, and codifying the four modes instead of a binary "enabled".

Important: in `ModeTUI`, **stdout is owned by bubbletea**. Subprocess stderr (devpod, ssh) is captured via `tea.Cmd` and surfaced inside the model (a viewport, a status line, or both). Anything that today writes directly to stdout from inside a long-run (the live devpod output during `drift new --debug`) is incompatible with `ModeTUI`; that path stays in `ModeColor`. We do not try to multiplex an external process's stdout into a bubbletea view.

### Theming

A single `ui.Theme` value, constructed once in `ui.DetectMode`, carries:

- `Accent`, `Success`, `Warn`, `Error`, `Dim`, `Muted`, `Bold` — `lipgloss.Style` values, not raw colors. Built from a `lipgloss.LightDark(isDark)` selector applied to a palette of hex pairs.
- `BorderFocused`, `BorderBlurred` — for panel framing in the dashboard.
- `KeyBinding`, `KeyDescription` — for help footers.

`colorprofile.Detect` runs first; the theme respects the detected profile so styles auto-downsample. `NO_COLOR=1` collapses every style to identity. There is exactly one place that knows the hex codes; everywhere else asks the theme.

A short integration test verifies that `NO_COLOR=1 drift status` produces zero ANSI bytes — this is a regression we've burned ourselves on before.

### Icons (Nerd Font)

The TUI assumes the terminal is running a Nerd Font and uses icon glyphs from the Nerd Font Private Use Area wherever an action, resource, or status can be expressed with one. Icons condense the interface — one glyph where a word would be — and give the TUI a distinct visual identity without shouting.

**Catalog (filled in during implementation; representative targets):**

- **Status:** running (play), stopped (square), stale (warning), unreachable (disconnected plug), starting (spinner-compatible), error (x-circle), success (check-circle).
- **Resources:** circuit (server / network), kart (container / package), chest (key / lock), character (person), tune (sliders / gear), port (plug), log (file-lines), skill (sparkle), run (play-square), ai (robot / spark).
- **Actions:** start (play), stop (stop), restart (rotate), rebuild (hammer), recreate (refresh-ccw), delete (trash), clone (git-branch), connect (arrow-right-to-bracket), migrate (arrow-right-arrow-left), enable (toggle-on), disable (toggle-off), add (plus), edit (pencil), save (floppy), filter (funnel), search (magnifier).
- **Navigation / chrome:** tab-switch chevrons, default-circuit star, expand/collapse carets, scroll-pos indicator, help (question), quit (power).
- **Tech/brand where appropriate:** github, docker, ssh, mosh, go — used sparingly for kart-source indicators or transport hints, never as decoration.

**Central registry.** All icon codepoints live in one place: `internal/cli/ui/icons.go`, as named `rune` constants (`IconStart`, `IconKart`, `IconCircuit`, etc.). Callers never write the raw codepoint inline. Makes it trivial to swap an icon (Nerd Font versions shift codepoints occasionally) or to maintain a parallel ASCII-fallback table.

**Fallback.** Not every terminal has a Nerd Font. Behavior:

- Default: ON. Drift's expected environments (Ghostty, kitty, iTerm2, Termux with a properly installed Nerd Font) handle this natively.
- Opt-out: `DRIFT_NO_NERDFONT=1` env var collapses every icon to its ASCII-fallback sibling from the same registry — a text word (`start`, `stop`), a short symbol (`▶`, `■`), or nothing at all depending on the icon's role. The tests exercise both paths via `DetectMode`'s carrying field.
- Auto-detection: deliberately NOT attempted. There's no reliable capability query for "does this terminal have a Nerd Font loaded." Guessing wrong (rendering tofu boxes everywhere) is worse than requiring an env var for the minority case.

**Sketches in this plan deliberately keep ASCII placeholders** (`●`, `○`, `▸`, `[tab]`, `arrow`, `clone`, etc.) for readability in the plan file and in PR diffs. Implementation replaces them with the registry icons. When reading the sketches, assume any status glyph, action label, or directional symbol will become a Nerd Font icon; the sketches fix what they represent, not how they render.

### Key bindings and help

`internal/cli/ui/keys.go` declares the shared bindings:

```go
var Keys = struct {
    Quit, Help, Filter, Refresh, Up, Down, Left, Right, Tab, ShiftTab,
    Enter, Escape key.Binding
}{
    Quit:    key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
    Refresh: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
    // ...
}
```

Every interactive model embeds `help.Model` and exposes a `KeyMap` with `ShortHelp()` / `FullHelp()` that includes its own bindings plus `Keys.Quit`/`Keys.Help`. Footers regenerate from the bindings — no string drift between code and what the user sees.

### Spinner and progress

`bubbles/spinner` replaces `briandowns/spinner`. The wrapper in `ui.spinner` exposes the same `Start(msg)` / `Succeed` / `Fail` shape so callers (`drift new`, `drift kart start`) don't churn. Two upgrades come for free:

- The spinner shares the theme — colors match the rest of the output.
- Long phases (>10s) auto-attach a `bubbles/progress` bar fed by a `tea.Tick` for the elapsed line, instead of the current "rewrite the suffix every second from a goroutine" pattern.

For multi-phase orchestrations (`drift new` does clone → up → dotfiles → finalize) we add `ui.PhaseTracker`: a stack of named phases, current phase rendered as `[2/4] running dotfiles...`, completed phases collapsed to dim checkmarks. This is the right shape for `drift kart rebuild` too.

### Prompts

`huh/v2` for every confirmation, single-select, and free-text prompt. A thin wrapper (`ui.Confirm(title)`, `ui.Select(title, options)`, `ui.Input(title, placeholder)`) so themes are applied centrally and the `WithAccessible` fallback is on by default — pipes and dumb terminals get plain stdin prompts instead of an error.

Composite flows (`drift migrate`, `drift init`) build their own `huh.Form` with multiple groups — these are kept thin, since the hard work is in the data plumbing, not the form rendering.

### Logs (`charmbracelet/log`)

`internal/slogfmt` is replaced by `github.com/charmbracelet/log`. The two solve the same problem (structured-record rendering for the human-facing surface) but charmbracelet/log is theme-aware, lipgloss-styled, light/dark adaptive, and ships a slog handler — it slots into the rest of the redesigned surface where the bespoke slogfmt does not.

What changes:

- `drift logs <name>` (one-shot tail) renders through `charmbracelet/log` instead of `slogfmt.Emit`. JSONL records from the server are decoded once, then either fed to the logger via `slog.Handler` or rendered directly with the logger's text formatter — whichever is simpler given the existing JSONL → record decode path. Plain (non-JSON) lines from the server are wrapped in an `Info` record with the line as the message field, mirroring today's behavior.
- The dashboard's logs tab uses the same logger, with output piped into a `bubbles/viewport`. Filter (`/`), level (`L`), and follow (`f`) interactions stay; the pretty rendering is now the logger's job, not a separate code path.
- Level parsing migrates from `slogfmt.ParseLevel` to a thin shim over `slog.Level` (both libraries speak slog levels; only the parser entry point changes).
- `internal/slogfmt` is deleted once the last caller migrates. Its tests are rewritten against the new logger or dropped if their concern (level filtering, level parsing) is now charmbracelet/log's job.

The migration is in scope for the same single PR as the rest of the presentation overhaul. It's small (the slogfmt API is narrow — `Record`, `Emit`, `ParseLevel`, `DecodeRecord`) and delivers an immediate consistency win: the same theme that drives spinners, tables, prompts, and dashboard panels now also drives log output. Server-side logs (lakitu) stay on whatever they emit — drift only owns the rendering.

### The `drift dashboard` TUI

A top-level subcommand: `drift dashboard [-c <circuit>] [--tab <name>]`. **Bare `drift` (no args) on a TTY opens the dashboard.** `drift menu` stays as an explicit subcommand for the launcher use case (pick a command, run it, exit) — the two are different jobs and both keep their place.

> **Sketches are for shape, not pixels.** Every ASCII mockup in this plan (dashboard chrome, banner placement, per-tab layouts produced during design) communicates *what lives where and how the user navigates it*. Exact borders, padding, spacing, color choices, glyph selection for focus/blur, and responsive breakpoints are the framework's job — lipgloss v2 decides joins and placement, `bubbles/help` decides footer shape, `bubbles/list` and `bubbles/table` decide their own chrome. When an implementation detail in a sketch conflicts with an idiomatic bubbletea/lipgloss approach, **follow the framework**. The plan locks information architecture and interaction model, not visual minutiae.

A long-lived bubbletea program. The tab bar sits at row 1 (always visible); the banner appears only on the **status** tab, inside the panel body.

**Chrome — every tab:**

```
┌─────────────────────────────────────────────────────────────────────┐
│  status  ▸ karts ◂  circuits  chest  characters  tunes  ports  logs │
├─────────────────────────────────────────────────────────────────────┤
│                                                                     │
│   <active panel content>                                            │
│                                                                     │
├─────────────────────────────────────────────────────────────────────┤
│ [tab] next  [1-8] jump  [/] filter  [r] refresh  [?] help  [q] quit │
└─────────────────────────────────────────────────────────────────────┘
```

**Status tab — full layout:**

```
┌──────────────────────────────────────────────────────────────────────────────────────┐
│ ▸ status ◂  karts  circuits  chest  characters  tunes  ports  logs                   │
├──────────────────────────────────────────────────────────────────────────────────────┤
│  ╮  •╭   drift 0.4.3                                              3 / 3   circuits   │
│ ╭┤╭╮╮┼┼  devpods for drifters                                     7 / 9   karts      │
│ ╰┴╯ ╰╯╰  [tagline tbd]                                                4   ports      │
│                                                                                      │
│  TIME      ACTION              KART                   DETAIL                         │
│  ──────────────────────────────────────────────────────────────────────────────      │
│   3m ago   drift new           alpha.plan-14          from example-org/template      │
│  12m ago   kart restart        alpha.api                                             │
│   1h ago   port add            alpha.web              :3000 → :3000                  │
│   2h ago   drift status        —                                                     │
│   3h ago   kart info           beta.experiments                                      │
│   4h ago   drift connect       alpha.api                                             │
│   5h ago   kart stop           alpha.experiments                                     │
│                                                                                      │
├──────────────────────────────────────────────────────────────────────────────────────┤
│ [tab] next  [1-8] jump  [enter] drill  [r] refresh  [?] help  [q] quit               │
└──────────────────────────────────────────────────────────────────────────────────────┘
```

The status tab has three regions:

- **Banner block** (top-left, 3 rows) — Tmplr Rounded wordmark with rainbow gradient + 3-line text lockup (`drift <ver>` bold, tagline + placeholder dim). Status tab only.
- **Stats block** (top-right, 3 rows, right-aligned) — small key-value table with numbers first. `circuits N/M` (reachable/total), `karts N/M` (running/total), `ports N` (count, no slash — ports never conflict on this circuit). Numbers right-aligned in their column, labels left-aligned after a 3-space gutter, whole block self-sized to the longest label and anchored to the panel right edge. Numbers bold, labels dim.
- **Activity table** (full-width, scrollable viewport) — TIME / ACTION / KART / DETAIL. TIME right-aligned and dim; ACTION is the verb (`drift new`, `kart restart`, `port add`, etc.); KART is `<circuit>.<kart>` for kart-scoped actions or `—` for global ones (`drift status`, `drift help`); DETAIL holds context where useful (clone source, port mapping, what changed). `enter` on a row jumps to the relevant resource (`kart restart api` → karts tab filtered to that kart). Source: in-memory ring per session, optionally backed by a small persisted ring at `~/.local/state/drift/activity` for cross-session scrollback (open question — cheap to add later).

**Banner.** Owned by the status panel, not the dashboard chrome. Top-left of the panel body on tab 1 only: a Tmplr Rounded ASCII wordmark for "drift", with a three-line text lockup beside it (name + version, tagline, third tagline placeholder). The banner glyphs get a horizontal rainbow gradient via `lipgloss.Blend1D`; the lockup text stays in `theme.Bold` (line 1) and `theme.Dim` (lines 2-3) so the wordmark owns the color and the text owns the meaning. Hardcoded as a `const` in `internal/cli/ui/dashboard/banner.go` — no runtime figlet renderer. Skipped entirely when `theme.Color == false` (a plain "drift 0.4.3" header replaces it) or when terminal width drops below the banner's footprint. Switching away from the status tab reclaims the 3 rows for panel content.

**Tab bar.** Row 1, always visible across every tab. `status · karts · circuits · chest · characters · tunes · ports · logs`. Active tab underlined in accent color, others dim. Switch with `tab` / `shift+tab` or numeric keys `1`–`8`. On widths below ~100 cols the bar collapses to numeric pips (`1 2 3 4 5 6 7 8`) with the active tab's name shown alone.

**Tabs:**

- **status** — high-level overview (see status-tab layout above). Banner + stats block in the top row; full-width activity table below. Stats refresh on a 10s ticker (latency, kart status); activity is event-driven, no polling. *Drill into karts/circuits tabs for detail* — the status tab deliberately doesn't reproduce them.
- **karts** — cross-circuit kart table, filterable (`/`), sortable, enter expands a row inline into a `drift kart info` key-value block. In-row lifecycle actions with confirmation modals: `s` start, `x` stop, `R` restart, `B` rebuild, `C` recreate, `D` delete, `c` connect (execs out), `l` jump to logs tab pre-filtered, `e`/`d` enable/disable autostart.
- **circuits** — circuit-level admin. Add (`a`, inline form), delete (`d`), set default (`space`), rename (`r`), drill into per-circuit detail (`enter`). On zero circuits the dashboard auto-opens the add modal (subsumes `drift init`'s "first circuit" path).
- **chest** *(read-only)* — env-refs per circuit. Table with name, backend, last-used, used-by-karts. `enter` expands to backend resolver detail + cross-link to kart references. No add/edit/rotate; that stays a CLI surface (`lakitu chest …` over ssh).
- **characters** *(read-only)* — git identity bindings per circuit. Name, git name, git email, PAT ref, kart count. `enter` expands to container username, dotfiles, kart references. Authoring stays in `lakitu character edit`.
- **tunes** *(read-only)* — workspace/session configs per circuit. Name, base image, feature count, used-by-karts. `enter` expands to full devcontainer fragment in a viewport. Authoring stays in `lakitu tune edit`.
- **ports** — implemented from scratch in this PR against plan 13's data layer (RPC surface + `~/.config/drift/ports.yaml` + the ssh ControlMaster lifecycle). Plan 13 ships as a plain-text/JSON CLI, not a TUI, so there's no upstream model to embed; the dashboard's ports tab is its own `Panel` consuming the same APIs the `drift ports` CLI calls. Likely outcome of integration: plan 13's API shape gets adjusted (richer list responses, structured remap info, event-style updates) so the TUI panel can render it without ad-hoc parsing — both halves land in this PR even if it means re-touching files plan 13 introduced. Add/remove forwards, see active forwards + remaps, conflict resolution flow.
- **logs** — kart picker on top, scrollable log viewport below. `/` filter, `L` cycle level, `f` toggle follow, `s` save current contents to `~/drift-logs-<kart>-<ts>.log`.

**Cross-tab affordances.** `:` opens a fuzzy command palette (every action in every tab); `?` opens the full-help modal; `Q` quits without confirming, `q` confirms if any in-flight action; `enter` on any expandable row toggles its detail. Toast region bottom-right for transient messages.

**Read-only resource panels are cheap.** Chest/characters/tunes share a `ResourcePanel[T]` generic — per-circuit grouped table + filter + expand-on-enter, one file in `dashboard/panels/`. Per-tab files are thin wrappers that supply the RPC fetcher and the row formatter, so three tabs cost roughly the LOC of one. RPC results cached for 30s (model-local, busted by `r`). If a tab's RPC isn't wired on the lakitu side yet, the panel renders a "not available on lakitu vX.Y.Z — needs vA.B.C" notice instead of erroring.

**Layout.** `lipgloss.JoinVertical` of banner + tab bar + active panel + footer; active panel composes its own internal layout. Each panel implements a small `Panel` interface (`Init`, `Update`, `View`, `Focused`, `KeyMap`) so the root model just routes messages and a future ninth tab is one new file.

**Refresh.** `r` for explicit, plus a 10s ticker for live data (status latency, karts list, ports). Pause the ticker when the program loses terminal focus (bubbletea v2 emits focus events; verify Termux passes them through).

**Startup.** Bare `drift` → `runDashboard`: load `~/.drift/config.json`, compute theme once, kick off parallel `status.Probe` for every circuit (errgroup, limit=4 — same call `drift status` makes today). First frame renders with placeholders; rows fill in as probes complete. Default tab `status` unless `--tab <name>` overrides. Non-TTY bare `drift` prints a one-liner pointing at `drift dashboard` / `drift help` and exits cleanly.

**Entrance animation.** When the dashboard opens (and only then — never on tab switches or refreshes), the status panel plays a short staggered slide-in driven by `github.com/charmbracelet/harmonica`. Spring physics, not linear easing — the wordmark *drifts* into place and settles, matching the product name.

Sequence:

1. **Banner** slides in from the left edge of the panel to its final position (~0 to ~9 cols), spring frequency 6.0, damping 0.5. Takes ~400ms to settle.
2. **Lockup** (drift 0.4.3 / tagline / placeholder) starts ~150ms after the banner begins, slides in from the right with the same spring constants. Each line is offset by ~50ms from the previous so they feel like they're catching up rather than entering as a block.
3. **Stats block** (top-right) starts ~250ms in, slides in from the right edge.
4. **Activity table** fades in last — opacity-style via dim → normal styling on the rows, no horizontal motion. ~600ms after launch the whole tab is settled.

Implementation: a single `tea.Tick(time.Second/60, ...)` loop while the entrance is running. Each tick calls `spring.Update(pos, vel, target)` for each animated element, rounds the float position to the nearest column, re-renders. When all springs have settled (velocity below a threshold) the loop ends and the dashboard is in its steady state. Total cost: a handful of float ops per frame for ~10 frames, then nothing.

**TUI gotcha.** Position resolution is one character cell, not one pixel. Spring positions are floats; rounding to integer columns means the motion steps in 1-col chunks at 60fps. That's fine for the entrance (the eye reads it as smooth motion), but it does mean we don't try to animate things smaller than a full column. No sub-pixel shimmer.

**Opt-out.** `DRIFT_NO_MOTION=1` or `--no-motion` skips the animation and renders the final frame immediately. Same env shape as the `prefers-reduced-motion` web convention. Auto-skipped when `theme.Color == false`, when terminal width is below the banner's footprint, when the program is reattaching to an existing alt-screen, and inside `teatest` runs (where deterministic frames matter more than aesthetics).

The animation is contained to the status tab's first paint. Other tabs render in their final state immediately when activated — no spring-motion on tab switches, that would cross over from "polished entrance" into "annoying every time."

### Per-command redesigns

Every existing surface gets a defined target. Unchanged commands omitted.

| Surface | Target mode | Notes |
|---|---|---|
| `drift dashboard` | TUI (new) | The flagship. |
| `drift menu` | TUI | Stays as an explicit subcommand for the launcher use case (pick a command → run → exit). Reimplemented on bubbles `list` with theme + help footer. **No longer the entry point for bare `drift`** — that now opens `drift dashboard` (see dashboard section). |
| `drift` (no args) | TUI | On a TTY, opens `drift dashboard`. Non-TTY prints a one-liner pointing at `drift dashboard` / `drift help` and exits. |
| `drift connect` (picker) | TUI | huh-based picker stays, themed; bare connect is unchanged. |
| `drift connect` (drift-check rebuild prompt) | Prompt | `ui.Confirm`, themed. |
| `drift migrate` | TUI | Three-step `huh.Form` becomes one form with progress indicator + preview pane on the side (candidate details, tune summary, character permissions). |
| `drift init` | TUI | Same pattern: composite form with progress dots; non-TTY error stays. |
| `drift new` (with flags) | Color (spinner + phases) | `PhaseTracker` showing clone / up / dotfiles / finalize; final success/fail line. `--debug` stays in Color (no TUI) because devpod stdout streams through. |
| `drift new` (bare) | TUI wizard | Today bare `drift new` errors asking for `--clone` or `--starter`. With a TTY, drop into a `huh.Form` wizard instead. Steps, in order: (1) name — `ui.Input` with live validation against existing karts on the chosen circuit; (2) source — `ui.Select` between `clone an existing repo` / `start from a starter` / `local path`, then a follow-up `ui.Input` with paste-friendly URL field and recent-clone history; (3) circuit — `ui.Select` over configured circuits, default preselected, hidden when only one circuit exists; (4) tune — `ui.Select` over circuit-side tunes (RPC-fetched, with a spinner during fetch), with descriptions; (5) character — `ui.Select` over characters; (6) optional advanced (`a` to expand) — devcontainer override, dotfiles URL, mounts, autostart toggle, features JSON; (7) review — read-only summary + `ui.Confirm`. On confirm, drops into the same Color spinner+phases path the flagged invocation uses; the wizard's role ends at `kart.new`. Cancellation at any step exits cleanly with no side effects. Non-TTY bare invocation keeps today's error (the wizard is opt-in via TTY presence, not a flag). |
| `drift kart start/stop/restart/recreate/rebuild` | Color (spinner + phases) | Same shape; rebuild gets the multi-phase tracker. |
| `drift kart delete` | Prompt + Color | Confirmation via `ui.Confirm`; spinner during deletion. |
| `drift list` / `drift karts` | Plain or Color | `ui.Table` decides; no model loop. JSON output preserved. |
| `drift status` | Plain or Color | Same. The *live* version is the dashboard's status tab. |
| `drift kart info` | Plain or Color | Key-value block via `ui.KeyValue` (new helper, two-column dim/accent rendering). The dashboard's karts-tab row-expand renders through the same helper. |
| `drift logs` | Plain or Color | Renders through `charmbracelet/log` (replacing `internal/slogfmt`). JSONL records → logger; plain text lines wrapped as `Info`. Under TUI the logs tab in the dashboard supersedes interactive use; same logger, output piped into a viewport. |
| `drift update` | Color (progress bar) | The hand-rolled `\r`-rewrite progress writer at `update.go:297` is replaced by `bubbles/progress` with a gradient fill, tied to a `ui.PhaseTracker` for the surrounding stages: check release → select asset → download → verify checksum → extract → atomic replace. Per-file extraction prints stay dim under the bar; in `ModePlain` the bar collapses to periodic byte-count lines so CI logs stay readable. `drift update <source>` (scp/url/local self-replace) shares the same tracker, with the source-resolution step as its first phase. |
| `drift kart enable` / `disable` | Plain or Color | Single success line via `ui.Status` so the glyph + theme matches every other lifecycle command. JSON output preserved. |
| `drift run` | TUI picker (no name) → exec | When invoked without a name on a TTY, the runs.yaml picker uses `ui.picker` (same shape as `drift connect`'s); with a name, drops straight into exec. The exec'd command keeps stdout — no model loop wraps it. |
| `drift ai` | TUI picker (no circuit) → exec | Circuit picker via `ui.picker`; then execs into Claude Code on the chosen circuit. Same pattern as `drift run` / `drift connect`. |
| `drift skill` | TUI picker + prompt → exec | No name → `ui.picker` over skills; name without prompt → `ui.Input` for the prompt; then executes on the circuit. The two-stage flow becomes one `huh.Form` with two groups so back-navigation works. |
| `drift circuit set name` | Color (spinner) | Server-side rename is a single RPC; wrap in `ui.Spinner` since the rewrite of server config + local alias takes a moment, and surface the before/after names in the success line. |
| `drift help` (short) / `drift version` | Plain or Color | Stay grep-friendly. `drift help --full` keeps its current Kong-derived catalog; only the column rendering moves to `ui.Table`. `drift version` keeps its plain-text and JSON outputs. |
| `drift help` / `drift help <topic>` | Plain or Color | Short help stays grep-friendly; `<topic>` mode renders markdown via `glamour` (style `auto` on TTY, `notty` otherwise). Optional follow-up. |
| `drift ports` | Plain or Color | Plain-text / JSON CLI per plan 13 — list / add / rm / show. Shares its data layer (state file + RPC + ssh ControlMaster lifecycle) with the dashboard's ports tab, but the two render independently. Per the standardization goal, plan 13's API shape may be adjusted in this PR to suit the dashboard panel. |
| Errors (everywhere) | All modes | `errfmt.Emit` keeps its job; the new `ui.Error(err)` wraps it so the rendered block uses theme colors and aligns with success lines. Inside a TUI, errors render into a status bar / toast and are also re-emitted to stderr on quit so they're not lost. |

### Dependency changes

Add (v2 import paths):

- `charm.land/bubbletea/v2`
- `charm.land/bubbles/v2`
- `charm.land/lipgloss/v2`
- `charm.land/huh/v2` (replaces `github.com/charmbracelet/huh` v1)
- `charm.land/colorprofile`
- `github.com/charmbracelet/log` (kept the GitHub path — replaces `internal/slogfmt`)
- `github.com/charmbracelet/harmonica` (spring physics for the dashboard entrance animation)
- (optional, later phase) `charm.land/glamour/v2`
- (test) `charm.land/x/teatest`

Remove:

- `github.com/briandowns/spinner` (replaced by `bubbles/spinner`)
- `github.com/charmbracelet/lipgloss` v1 (replaced by v2)
- `github.com/charmbracelet/bubbletea` v1, `bubbles` v0 (replaced by v2; both are currently indirect deps so the upgrade is clean)
- `github.com/charmbracelet/huh` v1 (replaced by v2)
- `internal/slogfmt` (replaced by `charmbracelet/log`)

Keep:

- `github.com/mattn/go-isatty` (still useful as a small, focused TTY detector; `golang.org/x/term` would also work but isatty is already pinned).

`go.sum` will churn. `make ci` covers `go mod tidy` regression.

### Demo mode + README GIF

The README ships an animated GIF of the dashboard rendered against canned fixture data, regenerated automatically and validated in CI so the asset can't silently drift from the real UI.

**Demo mode.** A hidden flag — `drift dashboard --demo` (or `DRIFT_DEMO=1`) — swaps the live RPC and config sources for a fixture loader. The fixture set lives in `internal/demo/fixtures/` and is *the same data the integration tests consume*: a small Go package (`internal/demo/fixtures.go`) exposes constructors that both the demo dashboard and integration tests import. One source of truth for "what does drift's world look like for testing." Editing a fixture (adding a circuit, a kart, an activity entry) changes both the integration test inputs and what the GIF shows — they can't disagree.

The demo path is otherwise indistinguishable from the live path: same theme, same tab routing, same key bindings, same teatest hooks. Only the data source is swapped. This means demo-mode bugs are real bugs.

**vhs tape.** A `docs/dashboard.tape` script drives the demo dashboard — opens the tab tour, scrolls the activity table, expands a kart row, jumps tabs, quits. Owned by the project; reviewed like code. `make readme-gif` runs `vhs docs/dashboard.tape` and writes `docs/dashboard.gif`, which the README references via `![dashboard](docs/dashboard.gif)`.

**Validation in CI.** Two-layer:

1. **Smoke** — the CI matrix runs `make readme-gif` and asserts it exits cleanly. Catches "the demo crashes" / "the tape script references a removed binding" / "vhs can't find a font" — all the build-level breakage.
2. **Frame equality** — a teatest test (`internal/cli/ui/dashboard/dashboard_demo_test.go`) replays the *same key sequence* the vhs tape uses against the same fixtures, captures rendered frames at every checkpoint, and golden-files them under `testdata/demo/`. This catches "the dashboard's output changed" without depending on vhs's non-deterministic GIF encoding. The teatest run is the actual regression gate; the vhs run is the docs build.

**GIF freshness.** The committed `docs/dashboard.gif` is treated as a binary docs asset, not a regression target. Devs regenerate it by hand (`make readme-gif`) when the UI changes meaningfully; PR reviewers can spot a stale GIF via the teatest goldens that *did* update. We don't gate CI on byte-equality of the GIF itself — vhs encoding has timing jitter and that fight isn't worth winning. If the goldens changed and the GIF didn't get regenerated, the worst case is a slightly outdated README, fixable in a one-line follow-up.

**Tooling additions:**

- `vhs` — installed in the dev shell (`flake.nix`) and the CI image.
- `make readme-gif` target — wraps the vhs invocation; checks for `vhs` on PATH; produces `docs/dashboard.gif`.
- `internal/demo/` package — fixture loader shared with `integration/`.

The make target and the demo flag are small additions to the giant PR. The vhs tape and the first GIF land alongside.

### Testing

The bubbletea framework unlocks a class of tests we don't have today. The lever is `charm.land/x/teatest`, which drives a `tea.Program` programmatically: scripted keystrokes in, captured frames out, golden-file diffs as assertions.

**Five test surfaces this gives us:**

1. **Golden-frame snapshots.** `teatest.NewTestModel(t, model, teatest.WithInitialTermSize(W, H))` runs the model with a pinned width/height. Send messages with `tm.Send(...)`, read frames with `tm.FinalOutput(t)` or via the read interface, diff against `testdata/<scenario>.golden`. `go test -update` regenerates baselines. Pinned terminal size is mandatory — otherwise frames flake across machines.

2. **Scripted user flows.** Replace "open the dashboard, click around, eyeball the result" with deterministic input. The dashboard happy-path test reads:

    ```go
    tm := teatest.NewTestModel(t, dashboardModel(...), teatest.WithInitialTermSize(120, 40))
    tm.Send(tea.KeyMsg{Type: tea.KeyTab})       // status → karts
    tm.Send(tea.KeyMsg{Runes: []rune{'/'}})     // open filter
    tm.Type("plan-14")                          // type filter text
    tm.Send(tea.KeyMsg{Type: tea.KeyEnter})     // expand row
    teatest.RequireEqualOutput(t, tm.FinalOutput(t))
    ```

3. **Fake clocks for tickers.** The status-tab latency ticker, logs-tab follow-poll, and toast-fade timer are all time-driven. Inject a clock interface (`type Clock interface { Now() time.Time; After(d) <-chan time.Time }`) so a 10s ticker fires immediately under test. Production wires `realClock{}`; tests wire a controllable fake. Eliminates `time.Sleep` from tests entirely.

4. **Component isolation.** Every panel is a standalone `tea.Model`, testable without spinning up the full dashboard. `panels/karts_test.go` constructs a karts panel with a mock RPC client + scripted messages, asserts on the returned model state — pure functional unit tests, no rendering, no goroutines. The full dashboard test only verifies tab routing.

5. **Property-style invariants.** Cheap to declare, expensive to forget. Run as table-driven tests across all panels via the shared `Panel` interface — write once, every new panel inherits coverage:
   - Every `key.Binding` has non-empty `Help()`.
   - `View()` output is ≤ program width (no horizontal overflow).
   - `Update(tea.WindowSizeMsg{...})` doesn't panic at any reasonable size.
   - Mock RPC errors surface as a visible error state (toast or modal), never silently swallowed.

**Plus the non-TUI tests we already need:**

- `DetectMode` matrix: every combination of `(stdoutTTY, JSON flag, NO_COLOR, NoTUI)` returns the expected `Mode`.
- `Theme` adaptive selection: light/dark backgrounds + the `colorprofile` levels (NoTTY / ANSI16 / ANSI256 / TrueColor) produce the expected style values.
- `NO_COLOR=1` regression: zero ANSI bytes in plain output for `drift status`, `drift list`, `drift kart info`. We've burned ourselves on this before.
- `progress_test.go` rewritten against the new spinner; behavior invariants preserved (no ANSI under non-TTY, success/fail emit the right glyph).
- Existing one-shot output snapshots (status formatting, error blocks, table rendering) stay as text-based asserts — cheap and effective for non-TUI surfaces.

**What still belongs in `make integration`:** the exec-into-shell paths (`drift connect`, `drift run`, `drift ai`) — bubbletea exits before the exec, so teatest covers up to the exec call only. Real ssh / mosh / devpod orchestration stays in `make integration`. Visual fidelity (does the rainbow look nice on Termux?) is human-eyeball territory; not gated.

**Test layout:**

```
internal/cli/ui/
  mode_test.go              DetectMode matrix
  theme_test.go             theme + colorprofile
  testkit/
    teatest.go              shared helpers (fixed-width fixtures, fake clock)
    nocolor.go              NO_COLOR assertion helper
internal/cli/ui/dashboard/
  dashboard_test.go         full-program teatest scenarios
  dashboard_demo_test.go    replays the docs/dashboard.tape key sequence (regression gate for the GIF)
  testdata/
    happy-path.golden
    karts-filter-restart.golden
    chest-expand.golden
    demo/                   golden frames for the README GIF replay
    ...
  panels/
    karts_test.go           component-level
    chest_test.go           ...
internal/demo/
  fixtures.go               shared test/demo data (also imported by integration/)
  fixtures_test.go
docs/
  dashboard.tape            vhs script driving --demo
  dashboard.gif             rendered output (binary asset, regenerated via `make readme-gif`)
```

## Delivery

One PR. The whole presentation layer flips at once: incremental migration of `internal/cli/style` / `internal/cli/progress` while leaving callers half-migrated would mean two themes, two spinner shapes, and two color paths coexisting in `main` for weeks — exactly the inconsistency this plan is trying to delete. Doing it as one PR also lets the dashboard land alongside the foundation it depends on, instead of stacking PRs that each block on the prior one.

What goes in the single PR, in implementation order (each step's diff stays reviewable on its own commit, but all ship together):

1. **Foundation.** Add `internal/cli/ui` (`Mode`, `Theme`, `Surface`, `Table`, `Header`, `Status`, `KeyValue`, `keys`). Bump `lipgloss` and `huh` to v2. Migrate every `internal/cli/style` caller. Delete `internal/cli/style`.
2. **Spinner / progress.** Add `ui.Spinner`, `ui.Progress`, `ui.PhaseTracker`. Migrate every `internal/cli/progress` caller (`drift new`, `drift kart start/stop/restart/recreate/rebuild`). Delete `internal/cli/progress`. Drop `briandowns/spinner`.
3. **Prompts.** Add `ui.Confirm`, `ui.Select`, `ui.Input`, themed `ui.picker`. Migrate `drift connect`, `drift kart delete`, `drift circuit add/rm/set`, `drift migrate`, `drift init`.
4. **Logs.** Wire `github.com/charmbracelet/log` through `drift logs`. Migrate the JSONL/plain-line decode path off `slogfmt.Emit` onto the new logger. Delete `internal/slogfmt` once the last caller is gone.
5. **Bubbletea infra.** `ui/tea.go` (program helpers, signal/ctx wiring), shared `viewport`. Reimplement `drift menu` on `bubbles/list`.
6. **Dashboard.** `drift dashboard` with all eight tabs (status, karts, circuits, chest, characters, tunes, ports, logs) and lifecycle actions on the karts tab wired through confirmation modals. Chest/characters/tunes are read-only and share a `ResourcePanel[T]` generic. The logs tab uses the charmbracelet/log path landed in step 4. The ports tab is implemented in this PR against plan 13's data layer (state file + RPC + ssh ControlMaster lifecycle); if plan 13's CLI-shaped API doesn't fit the dashboard's needs, adjust it as part of this PR — both halves ship together. Banner and Tmplr-Rounded const live in `internal/cli/ui/dashboard/banner.go`. Bare `drift` is rewired to open the dashboard.
7. **Demo mode + README GIF.** Add `internal/demo/fixtures.go`, the `--demo` flag on `drift dashboard`, the `docs/dashboard.tape` vhs script, and the `make readme-gif` target. Land the first `docs/dashboard.gif` and reference it in the README. Add the teatest demo-replay test that mirrors the tape's key sequence (the actual regression gate).
8. **Tests + cleanup.** `teatest` golden frames for the dashboard happy paths, `NO_COLOR` regression test, `DetectMode` matrix tests, dependency tidy. `make ci && make integration` green before merge.

Things deliberately deferred out of even the giant PR (would balloon the scope without paying for itself): `glamour`-rendered `drift help <topic>` (separate, optional), mouse-driven interaction in the dashboard. These are listed in non-goals or open questions; they can land as small follow-ups whenever someone wants them.

Risk of one PR: plan 13's CLI ships before this PR with an API shape that doesn't suit the dashboard's ports tab. Mitigation: plan 13 is plain-text/JSON only (no TUI to embed), so the data layer — state file format, RPC method shapes, ssh ControlMaster lifecycle — is the actual contract. Read what plan 13 lands and adjust those shapes in this PR if the dashboard panel benefits from richer responses or structured remap events. Pre-1.0 latitude (per the standardization goal) makes that fair game.

## Open questions

- **Termux background detection.** `lipgloss.HasDarkBackground` queries the terminal — Termux generally responds correctly, but Ghostty / dumb TERM combinations sometimes don't. Decide: trust the query, or expose `DRIFT_THEME=light|dark|auto` as an explicit override? (Lean: ship the env var on day one, default `auto`.)
- **Dashboard refresh cadence.** 10s ticker on every panel is cheap but noisy in a long-idle terminal. Pause the ticker when the program is in the background (terminal not focused) — bubbletea v2 exposes focus events; verify Termux passes them through.
- **glamour width on small terminals.** If `<topic>` help is wider than the terminal it word-wraps oddly. Worth testing on Termux landscape and tmux split panes before turning glamour on by default.
- **Third banner tagline.** The banner has a `[placeholder]` slot for a third line of dim text under "devpods for drifters". Naming this is cosmetic; pick before merge.
- **Tab count on narrow terminals.** Eight tabs collapse to numeric pips below ~100 cols, but Termux landscape is sometimes ~80; verify the collapsed-bar UX on real Termux before shipping. Worst case we hide chest/characters/tunes from the bar by default and surface them via the command palette.
- **Snapshot stability.** `teatest` golden frames are sensitive to terminal-width assumptions. Pin the test program width explicitly and accept that snapshot regen is part of any visual change.
