# Plan 14 — Charm-ecosystem UI research

Companion to `14-tui-redesign.md`. Surveys what popular Go TUIs built on the Charm stack actually do for chrome, navigation, lists, tables, forms, status, logs, motion, theming, icons, and animation. The point is to bias the plan-14 implementation toward conventions that already work in shipping apps, not to reinvent them.

The redesign target is on the v2 stack: `charm.land/bubbletea/v2`, `charm.land/lipgloss/v2`, `charm.land/bubbles/v2`, `charm.land/huh/v2`, plus `github.com/charmbracelet/harmonica` and (optionally) `charm.land/glamour/v2`. References below distinguish v1-era apps (most of the older Charm-team apps) from v2-era apps (Crush, tuios, much of the bubbles/v2 consumer set) because v2 changed enough that some "common patterns" only show up in the newer cohort.

## Projects surveyed

**Charm-team flagship apps.** `gum`, `glow`, `vhs`, `freeze`, `mods`, `soft-serve`, `wishlist`, `skate`, `melt`, `pop`. Most are still on bubbletea v1 / lipgloss v1; soft-serve and crush are the most architecturally instructive. Glow's pager is the canonical Charm "rich list + status bar + pager" reference.

**Charm-team v2 / modern.** `crush` (AI coding TUI, shipping on the full v2 stack today), the bubbles v2 examples themselves (`list-fancy`, `tabs`, `composable-views`, `help`, `pager`, `table-resize`, `progress-animated`), and the huh v2 `bubbletea` and `multiple-groups` examples.

**Community dashboards and dev tools.** `dlvhdr/gh-dash` (GitHub PR/issue dashboard, 11.5k stars, the closest analogue to drift's tabbed shape), `idursun/jjui` (Jujutsu VCS), `yorukot/superfile` (file manager, 17.2k stars), `Gaurav-Gosain/tuios` (tmux-like, already on bubbletea v2 + lipgloss v2), `robinovitch61/wander` (Nomad), `leg100/pug` (Terraform), `jonas-grgt/ktea` (Kafka, k9s-inspired, multi-cluster), `siddhantac/puffin` (hledger dashboard, sidebar tabs), `mathaou/termdbms` (databases, mode-driven), `caarlos0/tasktimer`, `bloznelis/typioca`, `awslabs/eks-node-viewer`. `lazygit` is gocui not bubbletea but every community bubbletea dashboard borrows from it.

**Modern non-bubbletea references.** `sst/opencode` (TS + Solid + opentui, structurally instructive), `anthropics/claude-code` (Ink/React, "scrollback-first" model), `aider` (Python, prompt-toolkit, REPL not TUI), `dagger/dagger` (migrated off bubbletea to `vito/tuist` for infinite-scrollback rendering), `serpl` (Rust + ratatui, redux-style state).

**Libraries and helpers.** `charm.land/bubbles/v2`, `charmbracelet/harmonica`, `lrstanley/bubblezone` (mouse hit-testing), `NimbleMarkets/ntcharts` (charts, sparklines, time-series), `charmbracelet/x/exp/charmtone` (the named Charm palette: Charple, Pepper, Iron, Squid, Mustard, Sriracha, etc.).

**Real-world v2 consumers** worth reading when you want production code rather than examples: `charmbracelet/crush`, `cli/cli` (gh, ships a custom huh `Field` for multi-select-with-search), `metaplay/cli` (`internal/tui/{compact_list,task_runner,progress}.go` is the cleanest "interactive + non-interactive same code path" pattern), `joshmedeski/sesh`.

## Layout and chrome

Two layout shapes recur across the ecosystem.

**The "welded tab strip" shape** is the canonical bubbletea look, set by `bubbletea/examples/tabs/main.go`. Inactive tabs use a rounded border with the bottom edge welded into the divider rule below them (`┴ ─ ┴`); the active tab opens its bottom into the body (`┘ space └`); the body box uses `UnsetBorderTop()` so the tabs' bottoms double as the body's top. Glow, gh-dash, soft-serve, superfile and crush all use a variant of this. Scope: ≤6 tabs comfortably, problematic above that on 80-column terminals. Drift's eight tabs sit right at the edge — the plan already calls out collapsing to numeric pips below ~100 cols, which is right.

**The "sidebar tabs" shape** appears once you go beyond ~6 sections or when tab labels are long. `puffin` puts navigation in a left sidebar (register / expenses / assets / liabilities / income / balance / accounts). `lazygit` uses fixed multi-pane with no top tab strip at all. For drift, the sidebar layout is a sensible fallback for narrow terminals, and the right shape for a future "command palette opens a sidebar" view.

**Vertical composition is always**: `JoinVertical(Left, header, tabs, body, footer)` with `tea.WindowSizeMsg.Height` minus `lipgloss.Height(headerView()) + Height(tabsView()) + Height(footerView())` allocated to the body. Each tab implements `SetSize(w, h)` and forwards to its components. Crush owns this rigorously; sloppier apps (older bubbletea examples) recompute heights inside the panel, which produces flicker on resize.

**Status bar at the bottom is a 5-zone hstack.** Glow's pager (`ui/pager.go`) and soft-serve's `pkg/ui/components/statusbar` converge on `Logo | Note (truncated to remaining width via ansi.Truncate) | spacer | Scroll % | Help`. The note zone is the toast slot: replace its content with a green-bg success or red-bg error variant for ~3s, then auto-revert via a `statusMessageTimeout` `tea.Cmd`. Bubbles' `list.NewStatusMessage(text)` already implements exactly this contract for list views. Drift should pick the segments (likely `Logo | Circuit | Toast | Latency | Help`) and use the same ansi-truncate width math.

**Header carries identity, not chrome.** Soft-serve's header is *just* `Styles.ServerName.Render(text)`. Crush's adds working-directory, key hints, and version, spread across the row with `lipgloss.PlaceHorizontal` and diagonal `╱` whitespace fillers. ktea adds a per-cluster color tint to the header so the entire chrome reflects which environment the user is on. Drift's plan already uses the status tab for the banner; adopting ktea's per-circuit tint (the header takes a slight bg cast keyed to the circuit color) is a strong safety move for multi-environment users.

## Tabs and navigation

The reference implementation is `charmbracelet/soft-serve/pkg/ui/components/tabs/tabs.go`: a tiny `tea.Model` with optional `• ` dot prefix on the active tab, `TabSeparator` style between, Tab and Shift-Tab plus mouse-zone clicks for navigation, and an `ActiveTabMsg` emitted on change. It's ~150 LOC and the cleanest tabs model in the catalogue.

**Numeric quick-switch is universal in the high-polish set.** `lazygit`, `tuios`, `gh-dash`, `crush`, and `superfile` all bind `1`–`9` to direct tab jumps. Drift's plan already includes this; it's table stakes.

**Numeric collapse on narrow terminals.** Below the threshold where full labels fit, both `tuios` and `crush` collapse the tab strip to numeric pips (`1 2 3 4 5 6 7 8`) with the active tab's name shown alone. This is what the plan describes; it's a good match.

**Mouse routing via `bubblezone`.** `lrstanley/bubblezone` is the de-facto solution: wrap each clickable region in a zero-width zone marker (`zone.Mark(id, ...)`); on mouse event scan for the matching zone. `tuios`, `fleetd`, and `soft-serve` all use it. Hand-rolling coordinate math is the older-app trap. The plan-14 dashboard should adopt bubblezone from day one if mouse is on the table at all.

**Page-stack vs tabs is a design choice, not a forced one.** `wander` (Nomad) navigates jobs → allocs → tasks → logs as a stack with `esc` popping back; tabs are implicit in the breadcrumb. drift's eight surfaces are flat and concurrent, so tabs are right; but the *karts* tab benefits from a stack-style drill-in (kart row → kart info → logs) inside the tab.

## Tables and lists

**`bubbles/list` with a custom delegate beats `DefaultDelegate` for almost any non-trivial app.** Two delegate flavours dominate: glow's hardcoded 2-line layout (gutter + icon + title on line 1; gutter + date/metadata on line 2; `│` gutter cursor recoloured per state), and wishlist's descriptor-function delegate where `Description()` calls a slice of *descriptor functions* and joins their styled output. The wishlist pattern is more reusable: each descriptor knows how to render one piece of metadata, and lists compose them.

**Custom list bypassing `list.Model` entirely** is what crush does (`internal/ui/list/list.go`, ~650 lines). It defines a tiny `Item` interface plus optional `FilterableItem`, `Focusable`, and `MatchSettable`, and reimplements filter/scroll. The reasons: (a) `list.Model`'s assumptions about footer chrome conflict with overlay dialogs; (b) match highlighting via raw ANSI underline avoids lipgloss style leakage into surrounding text; (c) selection behaviour is different in dialogs vs main panels. For drift, this is overkill until the dashboard wants behaviours `list.Model` doesn't support; the metaplay pattern (custom delegate, everything off, render title/help externally) covers most cases at low cost.

**`Height: 1, Spacing: 0` and turn off `ShowDescription`** for compact one-line lists. The default `DefaultDelegate` settings are sized for glow-style 2-line items; reusing them for the karts/ports lists makes everything feel airy and slow. metaplay's `compact_list.go` is the cleanest small recipe.

**Always swallow global keys while filter is active.** `m.list.FilterState() == list.Filtering` is the gate. Without it, typing `q` into the filter quits the app. The `list-fancy` example shows the right pattern.

**Filter-with-context, not filter-hides-all.** Bubbles' default filter behaviour collapses non-matches; `wander` highlights matches but keeps surrounding rows visible. Users repeatedly cite the default as disorienting. For drift's karts and ports lists, prefer the wander style: dim non-matches via `StyleLineFunc`, don't collapse them.

**`bubbles/table` is built on `viewport.Model`** so it gets vertical scroll keys for free. It does *not* support sortable columns natively; sort the underlying slice and `SetRows` again on key press. Per-cell coloring goes through `lipgloss.Table.StyleFunc(row, col)` on the lipgloss table package, *not* bubbles/table — see `bubbletea/examples/table-resize/main.go` for the canonical zebra-stripe + status-color recipe. At narrow widths, set elided columns' `Width = 0`; bubbles/table treats them as hidden.

**Selection styling: pick one, not both.** Either left-bar accent (`Border(NormalBorder(), false, false, false, true)` with `BorderForeground(focusColor)`) or background tint, never both. Crush uses the left bar everywhere; glow uses fuchsia bg for selection in the stash. The left bar keeps geometry stable across selection state, which is why Crush prefers it.

## Forms and prompts

**Reach for `huh.Form` first.** Every recent Charm-team app delegates form rendering to huh; even charmbracelet/skate's `main.go` ships a `// TODO: use huh` comment on its hand-rolled `fmt.Scanln` confirm. Drift's plan already wires every prompt through huh wrappers, which is right.

**The `Inline(true)` + fixed title width trick** (freeze's `interactive.go`) fakes a two-column "label: value" layout inside huh's vertical form. Each field gets `.Inline(true).Title(padTo("Theme", 18))`. Combined with a `MaxHeight(33)` and `WithTheme(huh.ThemeCharm())`, it's the reference shape for any settings dialog. Drift's `drift new` wizard and `drift migrate` flow can lift this directly.

**huh v2 layouts: `LayoutColumns(n)` and `LayoutGrid(rows, cols)`** unlock multi-pane forms without escape hatches (huh v1 was vertical-only). Drift's `drift new` wizard with a live `kart.yaml` preview pane on the right side maps directly onto the huh+bubbletea example (`huh/examples/bubbletea/main.go`): form left, preview right, joined with `JoinHorizontal`, both panels recompute on every form `Update`.

**Themed accessible fallback ships free.** `f.WithAccessible(true)` degrades to plain stdin prompts that work over screen readers and pipes. Drift should set this on by default and only opt out for genuinely interactive surfaces.

**Custom `huh.Field` for async/dynamic inputs.** gh's `internal/prompter/multi_select_with_search.go` is the production reference: implements `huh.Field` directly, holds a `textinput.Model` + `spinner.Model` + selection list, builds option list as `[selected first, then results, then persistent]` for stable UX during a search. Drift's tune/character pickers (which load over RPC) want this shape — the spinner is part of the field, not a separate state.

**`form.WithShowHelp(false).WithShowErrors(false)`** lets the dashboard render help and errors in its own footer instead of the form's. This is the integration pattern when huh sits inside a tab rather than as a standalone surface.

## Spinners, progress, and the "✓ done" line

**The `metaplay/cli` `TaskRunner` is the canonical "long task with check-mark line" implementation** (`internal/tui/task_runner.go`). Each phase has `StatusPending → Running (spinner) → Completed (✓) / Failed (✗)`, with elapsed time muted in brackets. The whole row keeps its position; only the symbol and trailing time change. Drift's `drift new` and `drift kart rebuild` should copy this verbatim:

```
○ clone repo
✓ devcontainer up [4.2s]
{spinner} dotfiles
○ finalize
```

becoming

```
✓ clone repo [0.8s]
✓ devcontainer up [4.2s]
✓ dotfiles [1.1s]
✓ finalize [0.3s]
```

The status icons live in the same column across all rows; the spinner is the same `bubbles/spinner.Spinner` that becomes a `✓` on completion.

**Interactive vs non-interactive in one function.** metaplay's `RunWithProgressBar` runs the same logic with two render paths: in non-interactive mode it logs once at start and once at end; in interactive mode it renders `\r{spinner} {label}... {current}/{total}` and clears with `\r\033[K` on exit, then logs the final `✓ {label} ({size}) [1.2s]`. Drift's `Mode` enum (Plain/Color/TUI) maps directly onto this; the call site doesn't fork.

**Progress bar gradients and per-cell coloring.** v2's `progress.WithDefaultBlend()` replaces v1's `WithGradient`/`WithDefaultGradient`. New: `WithColors(c1, c2, c3...)` for multi-stop, `WithColorFunc(func(total, current float64) color.Color)` for dynamic per-cell coloring (e.g. red as it approaches a quota), `WithScaled(true)` to stretch the blend across the filled portion only. ETA is computed from `time.Since(start) / m.progress.Percent() * (1 - m.progress.Percent())` since the bar doesn't ship one.

**Mods' cycling animation is the spiritual ancestor of everything in this category.** `charmbracelet/mods/anim.go` cycles random hex-y characters with a magenta→violet HCL-blended gradient, then degrades to an ellipsis spinner. Crush's `internal/ui/anim/anim.go` is the v2 evolution: pre-render N styled frames into a cache keyed by `xxh3(settings)`, advance an atomic step counter on tick. Way cheaper than re-styling per frame. Drift can lift Crush's `Anim` package directly for any indeterminate "kart is starting" / "RPC pending" state over ~500ms.

## Help and key bindings

**`bubbles/help` driven by a context-aware `keyMap`.** The `pop/keymap.go` pattern is correct: declare bindings as a struct with `key.WithKeys/WithHelp/WithDisabled`, and call `updateKeymap()` after every state change to enable/disable bindings based on whether they apply right now. `key.SetEnabled(false)` is the right way to hide a key contextually — `help` filters disabled bindings out of `ShortHelp/FullHelp` for free.

**Capital letters for destructive ops.** Crush uses `D` for delete, `K` for kill, `Y` (alongside `y`) for confirm-quit. Lowercase letters never trigger destructive actions. This is a community convention worth observing strictly.

**`?` toggles `m.help.ShowAll`.** Short help renders 6-ish keys on one line; full help expands to multi-column grouped output via `FullHelpView` joining columns with `JoinHorizontal(Top, sep, keys, " ", descs)`. Set `m.help.SetWidth(msgWidth)` from `WindowSizeMsg`; without it, short-help truncation doesn't kick in and the help line overflows.

**`DefaultKeyMap()` as a function returning a fresh struct.** v2's idiom (textinput, textarea, paginator all do this) avoids global-keymap leakage across multiple instances of the same widget. Drift should ship `keys.DefaultDashboardKeys()`, `keys.DefaultKartsKeys()`, etc., not package-level `var`s.

## Logs and viewports

**Viewport with `LeftGutterFunc` for line numbers / timestamps.** v2 added `SoftWrap`, `LeftGutterFunc` (gutter persists during horizontal scroll), `SetHighlights([][]int)` + `HighlightNext/Previous`, `StyleLineFunc func(int) lipgloss.Style`, and `FillHeight bool`. The reference is `bubbletea/examples/pager/main.go`: gutter line numbers, regex highlights with distinct `HighlightStyle` vs `SelectedHighlightStyle`, header/footer rules with `BorderRight = "├"` / `BorderLeft = "┤"` welding into the surrounding rule, scroll percent in the footer (`%3.f%%:%3.f%%` for vertical:horizontal). `HighPerformanceRendering` is gone in v2.

**Tail-follow is hand-rolled.** No built-in toggle. Track `m.viewport.AtBottom()`; on new content, if at bottom call `GotoBottom()`, otherwise leave the user where they are. Crush does this for its bash-tool stream.

**Initialise after `tea.WindowSizeMsg` arrives.** Pre-window viewports have zero width and silently render nothing; the pager example handles this with a `m.ready` flag. This trips up newcomers consistently.

**Search-within: `SetHighlights(regexp.FindAllStringIndex(content, -1))`** then `n`/`N` → `HighlightNext`/`Previous`. Filter mode dimming via `StyleLineFunc` returning a faint style for non-matching lines.

**`charmbracelet/log` for structured rendering** is in scope for plan-14 already (replacing `internal/slogfmt`). It ships a `slog.Handler`, is theme-aware, and slots into a viewport cleanly. The dashboard's logs tab should pipe `charmbracelet/log`-formatted output into a `bubbles/viewport`; the same logger drives `drift logs` in non-TUI mode.

## Modals, dialogs, and the command palette

**One file per dialog.** Crush's `internal/ui/dialog/{quit,permissions,commands,models,sessions,...}.go` and opencode's `component/dialog-*.tsx` independently converged on this. Replaces the older "monolithic update switch on a state enum" pattern. Drift's `internal/cli/ui/dashboard/dialog/{confirm,delete,kart_create,...}.go` should follow.

**Dialog stack, not single-modal.** Crush's `Overlay` (`internal/ui/dialog/dialog.go`) maintains a stack: `OpenDialog(NewError(...))` pushes, `CloseFrontDialog()` pops, `Update(msg)` only routes to the front. This handles the "delete kart → confirm modal → underlying error toast" composition cleanly. Drift's confirmation modals (kart delete, port-conflict resolution) want this shape.

**Centered double-bordered modal** is the community look (superfile, lazygit, jjui). `lipgloss.Place(termW, termH, Center, Center, dialogContent)` on a `RoundedBorder` style with `BorderForeground(focusColor)` and inner padding 1,2. Action buttons inline at the bottom of the body, separated by a styled gap.

**Crush's command palette is the bubbletea reference.** `internal/ui/dialog/commands.go` composes: `textinput` (filter), a custom `list.FilterableList` (filter-aware list with focus delegation), and `help.Model` for keystroke hints; tabs across categories (System / User / MCP) cycle on Tab/Shift-Tab; loading state for async commands handled by an in-dialog `spinner.Model` and a typed completion message. opencode's `dialog-command.tsx` is structurally identical (filter input + list + tabs), confirming this is the converged pattern.

For drift, the command palette is mentioned in the plan-14 cross-tab affordances ("`:` opens a fuzzy command palette"). Lift Crush's structure directly: filter input + filterable list + per-row item type + tabs across categories (Karts / Ports / Sessions / Tunes). The list-filterable abstraction can be lifted straight from `internal/ui/list/`.

## Status bars, toasts, and inline errors

**Three error-display tiers, used in combination.**

1. **Footer toast for non-blocking events.** `list.NewStatusMessage(text)` is the bubbles-shipped TTL pattern; Crush's `Status` styles (`ErrorIndicator`, `WarnIndicator`, `InfoIndicator`, `SuccessIndicator` + matching message styles) supply the visual vocabulary. Render `{indicator-icon} {message}` left-aligned in the status zone; the icon's bg is the status color, the message rides the normal status-bar bg. 3-second timeout is the convention (glow's `statusMessageTimeout`).

2. **Modal overlay for blocking errors and destructive confirmations.** Pushed onto the dialog stack, dismissable by Esc or any explicit key. Use for kart-delete confirmation, port-conflict resolution, fatal RPC errors that prevent further work.

3. **Inline footer error for transient validation.** The huh+bubbletea example renders form errors above the form via `appErrorBoundaryView` with `Foreground(red)`; clears as the error resolves. For form-shape inputs (kart create wizard), inline error in the huh form, plus footer toast on submit failure.

**Error styling has converged on three semantic colors.** Charm-team apps share pink `#FF5F87` for errors and inline code, teal `#00AF87` for links, purple `#6C50FF`/`#5A56E0` for brand/tabs. Both freeze and crush use truecolor → ANSI256 → ANSI three-tier color fallback (melt's `cmd/melt/main.go` is the reference) so the brand color doesn't go ugly on cheap terminals.

## Theme construction

Three observed theme shapes, in increasing sophistication. Drift's plan describes Tier 2; matching production polish will mean settling at Tier 3.

**Tier 1: flat semantic colors.** ~6-8 fully-resolved `lipgloss.Style` values (`Base, HeaderText, Status, Highlight, ErrorHeaderText, Help`) plus 3 named accent colors. The huh+bubbletea example. Right size for a single-screen app, undersized for a multi-tab dashboard.

**Tier 2: role-based with a semantic palette underneath.** A small palette of semantic tokens (`ColorBlue, ColorGreen, ColorRed, ColorNeutral, ColorOrange, ColorMuted`) feeds role-based render functions (`RenderTitle, RenderMuted, RenderError, RenderSuccess`). metaplay's `task_runner.go` is the cleanest small example. Right size for a single-feature tool.

**Tier 3: hybrid token-and-component.** Crush's `internal/ui/styles/styles.go` (~1500 lines):

- ~24 named palette tokens via `charmtone` (Charple, Dolly, Bok, Pepper, Charcoal, Iron, Ash, Squid, Smoke, Oyster, Sriracha, Zest, Malibu, Mustard, Julep, Coral, ...). The underlying tokens.
- ~12 semantic groupings: `primary, secondary, tertiary, accent`; backgrounds (`bgBase, bgBaseLighter, bgSubtle, bgOverlay`); foregrounds (`fgBase, fgMuted, fgHalfMuted, fgSubtle`); borders (`border, borderFocus`); status (`error, warning, info`).
- ~15 component-named sub-structs: `Header, CompactDetails, Markdown, FilePicker, Editor, Logo, Section, LSP, Sidebar, ModelInfo, Resource, Files, Messages, Tool, Dialog, Status, Completions, Attachments, Pills`. Each owns a field set of fully-resolved `lipgloss.Style` values plus `color.Color` slots for gradients.

For drift's eight-tab dashboard, Tier 3 is the right target. Sparser is fine for prototyping; denser without justification produces churn. Crush's structure ports directly: token palette in `internal/cli/ui/theme/palette.go`, semantic groupings in `theme.go`, component sub-structs colocated with their consumers (`dashboard/panels/karts/styles.go`, etc.).

**Always feed `isDark bool` once via `tea.BackgroundColorMsg`.** Don't call `compat.HasDarkBackground()` from a bubbletea program. v2 idiom: `tea.RequestBackgroundColor` in `Init`, listen for `tea.BackgroundColorMsg.IsDark()`, rebuild styles. `lipgloss.LightDark(isDark)(darkColor, lightColor)` returns the right color at runtime; the v1 `AdaptiveColor{Light, Dark}` struct still works but the function form composes better.

**ktea's per-environment color tint** is worth lifting. Each cluster gets a user-assigned hex color; the header/border picks it up so the chrome reflects which environment the user is on. For drift's circuits, the same applies: `circuit.color` in config, picked up by `theme.Header.BorderForeground` and `theme.Header.Background`. Cheap, dramatically reduces "I just deleted a kart in prod" surprise.

## Banner and branding

**No Nerd Fonts in any of the high-polish apps.** Crush's `internal/ui/logo/letterforms.go` is the standout: 419 lines of hand-tuned multi-row block-character glyphs (`▄ █ ▀ ╱`) for each letter, then `ForegroundGrad` paints the whole thing per-grapheme. Pure unicode block geometry. opencode's `logo.tsx` does the same in TS.

The reasons matter for drift: Nerd Font glyphs render as tofu (□) on Termux, on Windows console, on any system without a patched font. drift ships to Termux as a first-class platform. Crush's choice (zero Nerd Fonts, all Unicode block + geometric + arrows + single-letter severity codes) is the safe one. The plan-14 "Nerd Font on by default with `DRIFT_NO_NERDFONT=1` opt-out" is fine but **the default ASCII-fallback table needs to be richer than `start`/`stop`/`▶`/`■`** — Crush demonstrates that a polished look without Nerd Fonts is achievable with `✓ ⋯ ⟳ ◇ → ● × ◉ ○ │ ▌ ─ • ┃ ■ ≡` plus `E W I H` for severity.

**Per-grapheme horizontal gradient via `lipgloss.Blend1D`.** Crush's `internal/ui/styles/grad.go` (~70 lines) splits a string into grapheme clusters with `uniseg`, generates a color ramp, paints each cluster, joins back. This is the v2 idiom for the "drift" wordmark in the banner. The plan already specifies `lipgloss.Blend1D` for this; the implementation should lift Crush's `ForegroundGrad`.

**Per-letter handcrafted block-letterforms over figlet at runtime.** A hardcoded `const banner` (the plan says this) is right; what's worth adding: build the letterforms one-letter-per-rune-grid like Crush rather than as a single multi-line string, so the dashboard can scale to compact mode by selectively hiding letters and so a single letter can be randomly stretched horizontally as a subtle entrance-touch (Crush does this — one letter widens by 1 col on each render).

**Logo via `SetString` on a style.** Wishlist's trick: `Logo = lipgloss.NewStyle().SetString("Wishlist")`, then render via `styles.Logo.String()`. Cute and worth knowing.

## Animation and motion

**Harmonica is for spatial transitions, not decorative shimmer.** Verified consumers (via `gh search code "harmonica.NewSpring"`):

- `bubbles/progress` — the canonical v1 use, fill animation.
- `museslabs/kyma` — slide-deck transitions, frequency 7.0, damping 0.75, drives `t.x` from 0 to `-width` while compositing previous + next slide horizontally. The "page transition" recipe.
- `fleetd-sh/fleetd` — *the closest reference to a drift-style multi-pane dashboard*. Spring frequency 10.0, damping 0.8, drives `scrollY` toward `targetY` for springy focus-target scrolling. Multi-pane focus with bubblezone routing.
- `flyingrobots/go-redis-work-queue` — frequency 6.0, damping 0.25 for bouncy value readouts. Lower damping = more lively.

**Tunings in the wild: frequency 6-10, damping 0.25-0.8, FPS 60.** drift's plan-14 specifies frequency 6.0, damping 0.5 for the banner entrance; that's at the bouncy end and probably right for the wordmark. For panels-sliding-into-place a la fleetd, push to frequency 10, damping 0.8 — snappy with no bounce.

**Crush deliberately does not use harmonica.** Its motion is staggered character birth (per-cell delay up to 1s, randomized via `birthOffsets`) plus a 20fps gradient/character cycle. Predictable, cheap, prerenderable. Drift's banner-and-stats entrance is more like Crush than fleetd: spatial motion over ~600ms, then settled. The plan-14 spring choice is fine, but consider whether per-letter staggered birth would do the job more cheaply and look indistinguishable.

**Subtlety rule from the polished cohort:** harmonica for *transitions* (where motion has spatial meaning), pre-rendered frames for *idle/decorative* shimmer. Reserve springs for "the focused pane scrolls into view". Use frame caches for "Claude is thinking" / "kart is starting".

## Modern bubbletea v2 / lipgloss v2 idioms

Patterns that distinguish 2025-era TUIs from 2022-era:

- **Per-grapheme gradient text via `lipgloss.Blend1D`.** Crush's `ForegroundGrad`. 2022 apps used a single foreground.
- **Ambient theme detection via `tea.BackgroundColorMsg` + `lipgloss.LightDark(isDark)(dark, light)`.** Replaces the static `AdaptiveColor{Light, Dark}` struct.
- **Ultraviolet-style direct cell-buffer drawing for overlays.** Crush dialogs implement `Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor` instead of returning a `string` from `View()`. Avoids re-flowing the parent and lets you composite z-ordered layers. This may be premature for plan-14's first cut; worth knowing exists for Phase 2 if the modal stack ever fights the dashboard layout.
- **One file per dialog/component.** Replaces the monolithic update switch.
- **Pre-rendered animation frames keyed by a settings hash.** Crush's `internal/ui/anim/anim.go`. Build the gradient ramp once per (size, label, colors) tuple, render N styled frames into a `[][]string`, advance an atomic counter on each tick.
- **Event-driven rendering with explicit dirty flags.** Both `tuios` and `dagger/dagger` (post-bubbletea) call this out as a design correction. Don't re-render on every tick. Drift's 10s status-tab ticker is fine; the rest of the dashboard should render only on real state change.
- **`bubblezone` from day one** if mouse is supported at all.

Removed/changed in v2 that catches people:

- `lipgloss.Color` is now a function returning `image/color.Color`, not a `Color string` type. `var c lipgloss.Color = "21"` won't compile.
- `WithWhitespaceForeground` / `WithWhitespaceBackground` are gone; use `WithWhitespaceStyle`.
- `progress.WithGradient` / `WithDefaultGradient` → `WithColors(...color.Color)` / `WithDefaultBlend()`.
- `HighPerformanceRendering` is gone from viewport.
- Module path renamed: `github.com/charmbracelet/...` → `charm.land/.../v2`. Old blog posts and examples will use the github paths; the v2 stack is on `charm.land`.

## Specific recommendations for plan-14

These map onto the existing plan; nothing here contradicts it, but several recommendations sharpen it.

**Adopt a Crush-shaped theme.** Token palette → semantic groupings → component sub-structs. Aim for ~24 named tokens, ~12 semantic groupings, one sub-struct per component (Header, Tabs, Footer, Sidebar, Status, Karts, Ports, Logs, Dialog, Pills, Spinner). Anything sparser will feel inconsistent across eight tabs.

**Lift these crush packages directly:**

- `internal/ui/styles/grad.go` → `internal/cli/ui/theme/grad.go`. Per-grapheme `Blend1D` gradient.
- `internal/ui/anim/anim.go` → `internal/cli/ui/anim/`. Pre-rendered frame cache for "thinking" indicators.
- `internal/ui/list/{filterable,focus,item}.go` → `internal/cli/ui/list/`. Filterable list with focus callback. Use this for the command palette and any in-dialog list; keep `bubbles/list` for the main panels until it doesn't fit.
- `internal/ui/dialog/dialog.go` → `internal/cli/ui/dashboard/dialog/`. Dialog stack with `Overlay`. Each dialog one file.
- `internal/ui/logo/letterforms.go` → `internal/cli/ui/dashboard/banner/letterforms.go`. Per-letter block-character glyph definitions; "drift" assembled from these at compile time.

**Lift this metaplay package:**

- `internal/tui/task_runner.go` → `internal/cli/ui/tasks/runner.go`. Status-icon-in-fixed-column with elapsed time. Use for `drift new`, `drift kart rebuild`, `drift update`. Same code path serves Plain/Color/TUI modes; the renderer forks, the logic doesn't.

**Sharpen the plan in five places:**

1. **Per-circuit color tint on the chrome.** ktea's pattern. Add `color: "#..."` to the circuit config; theme reads it for `Header.BorderForeground` and a subtle `Header.Background` cast. Critical safety affordance for multi-environment users; small implementation cost.
2. **Filter-with-context, not filter-hides-all.** `wander`'s pattern. Override the bubbles/list filter behaviour for the karts and ports lists so non-matches dim rather than disappear. Users repeatedly cite the default as disorienting.
3. **Status tab activity table → page-stack drill-in.** The plan already says `enter` jumps to the relevant resource. Make this a stack (push/pop with `esc`) inside the karts tab specifically, since kart row → kart info → logs is a natural drill chain. Tabs stay flat; stack lives inside one.
4. **Compact-mode toggle.** `Ctrl+T` collapses padding, hides the banner, narrows the sidebar. `wander`, `gh-dash`, and `crush` all expose this. Termux landscape and tmux split-pane users will need it.
5. **Built-in named themes at v1.** Ship 2-3 from day one (catppuccin-flavored, gruvbox-flavored, default-charm). `superfile`'s plugin-themes ecosystem is the long-tail vision; `T` to cycle (termdbms) is a power-user delight. If the theme is one TOML file, this is trivial.

**Three traps to avoid that the plan currently sidesteps but that production apps stumble into:**

1. **Don't re-render on every `tick.Cmd`.** The 10s status ticker is fine; don't add tickers to other panels just because they could refresh. Render on real state change only, dirty-flag style.
2. **Don't ship one giant always-on help footer.** Short context-sensitive footer (one line, ~6 keys max) plus full help overlay on `?` via `bubbles/help` short/long mode is the universal pattern. The plan already does this; just don't drift toward "just one more key in the footer."
3. **Don't trust `os.Executable()` on Termux** (already in CLAUDE.md but worth flagging in any dashboard work that touches binary self-paths) and **don't trust Nerd Font availability** even when the user looks like they have a Nerd Font installed. `DRIFT_NO_NERDFONT=1` should produce a polished result, not a degraded one. Ship the rich ASCII-fallback table from day one.

## Sources

Charm v2 stack:

- [charmbracelet/bubbletea v2 examples](https://github.com/charmbracelet/bubbletea/tree/main/examples) — `tabs`, `list-fancy`, `composable-views`, `pager`, `table-resize`, `progress-animated`, `help`, `paginator`, `stopwatch`, `textinputs`.
- [charmbracelet/bubbles UPGRADE_GUIDE_V2.md](https://github.com/charmbracelet/bubbles/blob/main/UPGRADE_GUIDE_V2.md) and [lipgloss UPGRADE_GUIDE_V2.md](https://github.com/charmbracelet/lipgloss/blob/main/UPGRADE_GUIDE_V2.md).
- [charmbracelet/huh examples](https://github.com/charmbracelet/huh/tree/main/examples) — `bubbletea`, `multiple-groups`.
- [charmbracelet/harmonica](https://github.com/charmbracelet/harmonica) — spring physics; `examples/spring/tui/main.go`.

Charm-team apps (architectural references):

- [charmbracelet/crush](https://github.com/charmbracelet/crush) — `internal/ui/{styles,anim,logo,list,dialog}/`. The v2-era reference.
- [charmbracelet/soft-serve](https://github.com/charmbracelet/soft-serve) — `pkg/ui/components/{tabs,statusbar,footer,header,selector}/`. Component decomposition.
- [charmbracelet/glow](https://github.com/charmbracelet/glow) — `ui/{pager,stash,stashitem}.go`. Status bar + 2-line list delegate.
- [charmbracelet/freeze](https://github.com/charmbracelet/freeze) — `interactive.go`, `style.go`, `help.go`. huh-driven settings, branded help screen.
- [charmbracelet/mods](https://github.com/charmbracelet/mods) — `anim.go`. Cycling-rune animation ancestor.
- [charmbracelet/wishlist](https://github.com/charmbracelet/wishlist) — `listitem.go`, `styles.go`. Descriptor-function list delegate.
- [charmbracelet/pop](https://github.com/charmbracelet/pop) — `keymap.go`. Context-sensitive `key.SetEnabled`.
- [charmbracelet/melt](https://github.com/charmbracelet/melt) — three-tier color fallback; TTY-vs-pipe styling fork.
- [charmbracelet/vhs](https://github.com/charmbracelet/vhs) — `style.go`. Named-semantic-role style sheet.
- [charmbracelet/gum](https://github.com/charmbracelet/gum) — `style/lipgloss.go`. Flag → style translation table.

Community dashboards and dev tools:

- [dlvhdr/gh-dash](https://github.com/dlvhdr/gh-dash) — tabbed PR/issue dashboard. YAML-defined sections, bindable per-row actions.
- [Gaurav-Gosain/tuios](https://github.com/Gaurav-Gosain/tuios) — already on bubbletea v2 + lipgloss v2; command palette, workspace pips, BSP layout.
- [yorukot/superfile](https://github.com/yorukot/superfile) — themeable file manager, pill-shaped tabs, plugin themes.
- [robinovitch61/wander](https://github.com/robinovitch61/wander) — Nomad TUI; filter-with-context, compact-mode, page-stack nav.
- [leg100/pug](https://github.com/leg100/pug) — Terraform/OpenTofu; multi-select, hierarchical tree.
- [jonas-grgt/ktea](https://github.com/jonas-grgt/ktea) — Kafka; per-cluster color tint, k9s-style `:` command bar.
- [siddhantac/puffin](https://github.com/siddhantac/puffin) — hledger; left-sidebar tabs, locked-vs-scoped filters.
- [idursun/jjui](https://github.com/idursun/jjui) — Jujutsu; ace-jump, togglable preview overlay.
- [mathaou/termdbms](https://github.com/mathaou/termdbms) — mode-driven, theme cycling on `T`.
- [caarlos0/tasktimer](https://github.com/caarlos0/tasktimer) — tight single-screen polish.
- [bloznelis/typioca](https://github.com/bloznelis/typioca) — aesthetic ceiling for restrained color use.

Production v2 consumers (read these for non-example code):

- [charmbracelet/crush](https://github.com/charmbracelet/crush).
- [cli/cli internal/prompter](https://github.com/cli/cli/blob/trunk/internal/prompter/multi_select_with_search.go) — custom huh `Field` with async loading.
- [metaplay/cli](https://github.com/metaplay/cli) — `internal/tui/{compact_list,task_runner,progress}.go`.
- [joshmedeski/sesh](https://github.com/joshmedeski/sesh) — `picker/tui.go`. Tiny custom fuzzy picker.

Libraries:

- [lrstanley/bubblezone](https://github.com/lrstanley/bubblezone) — mouse hit-testing.
- [NimbleMarkets/ntcharts](https://github.com/NimbleMarkets/ntcharts) — sparklines, time-series, heatmap, OHLC.
- [charmbracelet/x/exp/charmtone](https://github.com/charmbracelet/x) — named Charm palette.

Modern non-bubbletea references (for ideas, not code):

- [sst/opencode](https://github.com/sst/opencode) — TS + Solid + opentui. One-file-per-dialog convention, theme-list dialog as a primary surface.
- [anthropics/claude-code](https://github.com/anthropics/claude-code) — Ink/React. Scrollback-first model, inline permission cards.
- [aider](https://github.com/Aider-AI/aider) — Python prompt-toolkit. Slash-command grammar, scrollback-first.
- [dagger/dagger](https://github.com/dagger/dagger) — migrated from bubbletea to `vito/tuist`. Per-component dirty-flag rendering.
- [yassinebridi/serpl](https://github.com/yassinebridi/serpl) — Rust + ratatui. Redux-style state organisation.
- [museslabs/kyma](https://github.com/museslabs/kyma) — slide-deck TUI, harmonica swipe transition.
- [fleetd-sh/fleetd](https://github.com/fleetd-sh/fleetd) — closest harmonica + multi-pane dashboard reference.
