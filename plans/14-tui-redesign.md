# drift TUI redesign — bubbletea-first presentation layer

## Problem

Drift's CLI presentation grew organically. Each subcommand reaches for what was nearby at the time: `briandowns/spinner` for long-runs, `mattn/go-isatty` + a hand-rolled `style.Palette` for color, ad-hoc `lipgloss/table` for tables, `huh.Select` for pickers, `text/tabwriter` as a non-TTY fallback, raw `fmt.Fprintln` everywhere else. The result works but is inconsistent: every command renders its own header shape, error indents differ between `errfmt` and inline `fmt.Errorf` paths, the spinner library doesn't share a palette with `lipgloss`, and there is no place to put richer interactive surfaces (multi-pane status, live log tails, port management) when they arrive. Plan 13 (`drift ports`) already calls for a real bubbletea TUI, and there will be more (a `drift dashboard` is the obvious next one). Without a shared presentation layer, every new TUI re-invents key bindings, theming, and TTY-fallback discipline.

This plan unifies the presentation layer on the Charm v2 ecosystem (`charm.land/bubbletea/v2`, `bubbles/v2`, `lipgloss/v2`, `huh/v2`, optional `glamour/v2`), retires the bespoke spinner/style packages where bubbles equivalents are stronger, and lands a flagship `drift dashboard` TUI with tabs as the showcase surface.

## Goals

1. **One presentation package.** All user-facing rendering — colors, tables, spinners, progress bars, prompts, headers, errors — flows through a single `internal/cli/ui` package built on lipgloss v2 + bubbles v2. Subcommand code asks for "the success line", "the kart table", "the connect picker"; it never reaches for raw lipgloss or bubbletea directly.
2. **TTY/non-TTY parity.** Every surface has both a bubbletea path (when stdout is a TTY and `--no-tui` / `--output json` are not set) and a deterministic plain-text path for pipes, CI, and scripted callers. The plain path is line-buffered, ANSI-free under `NO_COLOR`, and stable across releases. We never rely on bubbletea's nil renderer to fake non-interactive output.
3. **Adaptive theming.** One palette, computed once at startup from `lipgloss.HasDarkBackground` + `LightDark`, with explicit overrides for Termux (often light, often 256-color, sometimes 16-color) and dumb terminals. `colorprofile` downsamples; `NO_COLOR` strips entirely.
4. **Self-documenting key bindings.** Every interactive surface declares its keys as `bubbles/key.Binding` with help text and renders its footer via `bubbles/help`. No hand-typed `[q] quit  [/] filter` strings.
5. **A `drift dashboard` TUI.** A new long-lived bubbletea program that gives a tabbed live view of every circuit, kart, and port forward in one place. Replaces the "open three terminals to run `drift status`, `drift karts`, `drift ports list` and tail logs" workflow.
6. **Backwards-compatible CLI surface.** No subcommand renames, no flag breakage. `--output json` keeps its exact current shape (it's already a stable contract for scripts). Visual changes are unconditional under TTY; opt-out is `--no-tui` / `DRIFT_NO_TUI=1` for the bubbletea path specifically (still colorized output, just no live model loop).

## Non-goals

- **Replacing `--output json`.** JSON stays byte-identical. This plan touches the *human* surface only.
- **A driftd / long-lived background process.** The dashboard is a foreground program the user runs and quits. State for live views comes from existing RPC + `~/.config/drift/ports.yaml` (plan 13). No new daemons.
- **Replacing `internal/slogfmt`.** `charmbracelet/log` would be prettier but the migration cost doesn't pencil; slogfmt stays. Logs rendered inside bubbletea views go through the same slog handler.
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
- `ModeTUI` — stdout is a TTY, color on, full bubbletea program with alt-screen. Used by `drift dashboard`, `drift menu`, `drift ports` (interactive), `drift connect`'s picker, `drift migrate`'s wizard.

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

### The `drift dashboard` TUI

A top-level subcommand: `drift dashboard [-c <circuit>] [--tab <name>]`.

A long-lived bubbletea program with five tabs along the top:

```
┌ drift dashboard ─────────────────────── alpha · beta · gamma ─┐
│  status  ▸ karts ◂  circuits   ports   logs                   │
├───────────────────────────────────────────────────────────────┤
│                                                               │
│   CIRCUIT  NAME              STATUS    SOURCE      LAST USED  │
│   alpha    drift-app-server  running   github:...  2m ago     │
│   alpha    plan-14           stopped   local       3h ago     │
│   beta     experiments       running   github:...  1d ago     │
│                                                               │
│                                                               │
├───────────────────────────────────────────────────────────────┤
│ [tab] switch tab  [↑↓] move  [enter] open  [r] refresh  [q] quit │
└───────────────────────────────────────────────────────────────┘
```

Tabs:

- **status** — circuit roster, live latency every 10s, lakitu version, API reachability. Same data as `drift status` today, but live.
- **karts** — cross-circuit kart table, filterable (`/`), sortable, enter on a row opens a side panel with `drift kart info` shape; `s`/`x`/`R`/`D` start/stop/restart/delete (with confirmation modal). Effectively a console for the lifecycle commands.
- **circuits** — list of configured circuits with add/remove/set-default actions, default circuit highlighted, `a` adds via inline `huh` form.
- **ports** — plan 13's port view, embedded. Add/remove forwards, see conflict status, see remaps. Reuses the same model the standalone `drift ports` TUI uses.
- **logs** — pick a kart, tail its logs in a viewport, `/` filters, `[level]` filters.

Tab switching: `tab` / `shift+tab` / numeric keys (`1`–`5`).
Refresh: `r` for explicit, plus a 10s ticker for status/karts/ports.
Layout: `lipgloss.JoinVertical` of header + tab bar + active panel + footer; active panel composes its own internal layout. Each panel implements a small `Panel` interface (`Init`, `Update`, `View`, `Focused`, `KeyMap`) so the root model just routes messages.

The dashboard is the showcase, not the entry point — `drift` with no args still drops into `drift menu` (which now invokes the dashboard as one of its top options). The dashboard never replaces the one-shot commands; users who want to script will keep using `drift status --output json`.

### Per-command redesigns

Every existing surface gets a defined target. Unchanged commands omitted.

| Surface | Target mode | Notes |
|---|---|---|
| `drift dashboard` | TUI (new) | The flagship. |
| `drift menu` | TUI | Reimplemented on bubbles `list` with theme + help footer; gains the dashboard as an entry. |
| `drift connect` (picker) | TUI | huh-based picker stays, themed; bare connect is unchanged. |
| `drift connect` (drift-check rebuild prompt) | Prompt | `ui.Confirm`, themed. |
| `drift migrate` | TUI | Three-step `huh.Form` becomes one form with progress indicator + preview pane on the side (candidate details, tune summary, character permissions). |
| `drift init` | TUI | Same pattern: composite form with progress dots; non-TTY error stays. |
| `drift new` | Color (spinner + phases) | `PhaseTracker` showing clone / up / dotfiles / finalize; final success/fail line. `--debug` stays in Color (no TUI) because devpod stdout streams through. |
| `drift kart start/stop/restart/recreate/rebuild` | Color (spinner + phases) | Same shape; rebuild gets the multi-phase tracker. |
| `drift kart delete` | Prompt + Color | Confirmation via `ui.Confirm`; spinner during deletion. |
| `drift list` / `drift karts` | Plain or Color | `ui.Table` decides; no model loop. JSON unchanged. |
| `drift status` | Plain or Color | Same. The *live* version is the dashboard's status tab. |
| `drift kart info` | Plain or Color | Key-value block via `ui.KeyValue` (new helper, just two-column dim/accent rendering). |
| `drift logs` | Plain or Color | Plain unchanged; under TUI the logs tab in the dashboard supersedes interactive use. |
| `drift help` / `drift help <topic>` | Plain or Color | Short help stays grep-friendly; `<topic>` mode renders markdown via `glamour` (style `auto` on TTY, `notty` otherwise). Optional follow-up. |
| `drift ports` | TUI (plan 13) | Standalone TUI; same model embedded into dashboard's ports tab. |
| Errors (everywhere) | All modes | `errfmt.Emit` keeps its job; the new `ui.Error(err)` wraps it so the rendered block uses theme colors and aligns with success lines. Inside a TUI, errors render into a status bar / toast and are also re-emitted to stderr on quit so they're not lost. |

### Dependency changes

Add (v2 import paths):

- `charm.land/bubbletea/v2`
- `charm.land/bubbles/v2`
- `charm.land/lipgloss/v2`
- `charm.land/huh/v2` (replaces `github.com/charmbracelet/huh` v1)
- `charm.land/colorprofile`
- (optional, later phase) `charm.land/glamour/v2`
- (test) `charm.land/x/teatest`

Remove:

- `github.com/briandowns/spinner` (replaced by `bubbles/spinner`)
- `github.com/charmbracelet/lipgloss` v1 (replaced by v2)
- `github.com/charmbracelet/bubbletea` v1, `bubbles` v0 (replaced by v2; both are currently indirect deps so the upgrade is clean)
- `github.com/charmbracelet/huh` v1 (replaced by v2)

Keep:

- `github.com/mattn/go-isatty` (still useful as a small, focused TTY detector; `golang.org/x/term` would also work but isatty is already pinned).

`go.sum` will churn. `make ci` covers `go mod tidy` regression.

### Testing

- `internal/cli/ui` gets unit tests for `DetectMode` across the JSON / Plain / Color / TUI matrix and for `Theme` adaptive palette selection.
- `bubbles/help` keymap correctness is verified in panel tests via a small assertion that `ShortHelp()` matches the actual key bindings.
- `teatest` golden-frame tests for the dashboard happy paths: load → switch tab → quit; load → karts tab → filter → enter on a row → confirm restart. Frames stored under `internal/cli/ui/dashboard/testdata/`.
- A `NO_COLOR` integration test asserts zero ANSI bytes in plain output for `drift status`, `drift list`, `drift kart info`.
- Existing `progress_test.go` snapshots get rewritten against the new spinner; behavior invariants (no ANSI under non-TTY, success/fail emit the right glyph) are preserved.

## Delivery

One PR. The whole presentation layer flips at once: incremental migration of `internal/cli/style` / `internal/cli/progress` while leaving callers half-migrated would mean two themes, two spinner shapes, and two color paths coexisting in `main` for weeks — exactly the inconsistency this plan is trying to delete. Doing it as one PR also lets the dashboard land alongside the foundation it depends on, instead of stacking PRs that each block on the prior one.

What goes in the single PR, in implementation order (each step's diff stays reviewable on its own commit, but all ship together):

1. **Foundation.** Add `internal/cli/ui` (`Mode`, `Theme`, `Surface`, `Table`, `Header`, `Status`, `KeyValue`, `keys`). Bump `lipgloss` and `huh` to v2. Migrate every `internal/cli/style` caller. Delete `internal/cli/style`.
2. **Spinner / progress.** Add `ui.Spinner`, `ui.Progress`, `ui.PhaseTracker`. Migrate every `internal/cli/progress` caller (`drift new`, `drift kart start/stop/restart/recreate/rebuild`). Delete `internal/cli/progress`. Drop `briandowns/spinner`.
3. **Prompts.** Add `ui.Confirm`, `ui.Select`, `ui.Input`, themed `ui.picker`. Migrate `drift connect`, `drift kart delete`, `drift circuit add/rm/set`, `drift migrate`, `drift init`.
4. **Bubbletea infra.** `ui/tea.go` (program helpers, signal/ctx wiring), shared `viewport`. Reimplement `drift menu` on `bubbles/list`.
5. **Dashboard.** `drift dashboard` with all five tabs (status, karts, circuits, ports, logs) and lifecycle actions wired through confirmation modals. Ports tab embeds the same model plan 13's standalone `drift ports` TUI uses — coordinate the merge order with plan 13 so the import isn't a phantom; if plan 13 lands first, this PR consumes it; if not, this PR ships the ports panel against a stub interface that plan 13 implements.
6. **Tests + cleanup.** `teatest` golden frames for the dashboard happy paths, `NO_COLOR` regression test, `DetectMode` matrix tests, dependency tidy. `make ci && make integration` green before merge.

Things deliberately deferred out of even the giant PR (would balloon the scope without paying for itself): `glamour`-rendered `drift help <topic>` (separate, optional), mouse-driven interaction in the dashboard, replacing `internal/slogfmt` with `charmbracelet/log`. These are listed in non-goals or open questions; they can land as small follow-ups whenever someone wants them.

Risk of one PR: a bad merge ordering with plan 13 (ports). Mitigation: define the `Panel` interface in this PR, have plan 13's `drift ports` TUI implement it from day one, and review the two PRs together if they're both in flight.

## Open questions

- **Termux background detection.** `lipgloss.HasDarkBackground` queries the terminal — Termux generally responds correctly, but Ghostty / dumb TERM combinations sometimes don't. Decide: trust the query, or expose `DRIFT_THEME=light|dark|auto` as an explicit override? (Lean: ship the env var on day one, default `auto`.)
- **Dashboard refresh cadence.** 10s ticker on every panel is cheap but noisy in a long-idle terminal. Pause the ticker when the program is in the background (terminal not focused) — bubbletea v2 exposes focus events; verify Termux passes them through.
- **`drift menu` vs `drift dashboard`.** Currently `drift` with no args opens the menu. Consider making it open the dashboard once the dashboard is mature — the menu becomes redundant. Not a phase-1 decision.
- **glamour width on small terminals.** If `<topic>` help is wider than the terminal it word-wraps oddly. Worth testing on Termux landscape and tmux split panes before turning glamour on by default.
- **Snapshot stability.** `teatest` golden frames are sensitive to terminal-width assumptions. Pin the test program width explicitly and accept that snapshot regen is part of any visual change.
