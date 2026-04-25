# Plan 14 progress

**Status: closed out, superseded by `plans/16-dashboard-rebrand.md` for the dashboard rebrand work.** The foundation (`internal/cli/ui` package, mode/theme/surface, eight-tab dashboard skeleton, harmonica entrance animation, snapshot tests, demo mode, `make eval-frames`) is landed and stays. Remaining items in the "Still to do" section below are re-bucketed by plan 16 into either (a) folded into the rebrand, or (b) deferred to follow-up plans (lifecycle action wiring, `drift new` wizard, glamour help, etc.). Continue work on `feat/plan-14-fresh`; no new worktree.

Current branch: `feat/plan-14-fresh` in `.claude/worktrees/plan-14-fresh`. Last `make ci` was green; the dashboard renders end-to-end against demo and live data sources, debug logs no longer punch through the alt-screen, and the status panel has the harmonica-driven entrance animation.

## Done

**Foundation (`internal/cli/ui`).** `Mode` + `DetectMode`, `Theme` with adaptive light/dark via `lipgloss.LightDark` and a `DRIFT_THEME` override, `Surface` carrying mode + theme + writers, `Header` / `Status` (success/warn/fail) / `KeyValue` / `Table` renderers, `CellStyle` enum, shared `key.Binding` registry, unicode-glyph `icons.go` catalog with `DRIFT_NO_NERDFONT` fallback, `tea.go` program helpers, prompt wrappers (`Confirm` / `Select` / `Input` / `Pick`), `Spinner` / `Progress` / `PhaseTracker` on bubbles v2.

**Dependency churn.** `lipgloss`, `bubbles`, `bubbletea`, `huh` all on `charm.land/.../v2`. `colorprofile`, `charmbracelet/log`, `charmbracelet/harmonica` added. `briandowns/spinner` and `lipgloss` v1 dropped.

**Retired packages.** `internal/cli/style` (every caller migrated to `*ui.Theme`), `internal/cli/progress` (every caller migrated to `ui.Spinner` / `ui.PhaseTracker`), `internal/slogfmt` (drift logs routes through `charmbracelet/log`; server-side filter pushdown inlined a 12-line `parseLogLevel` + `decodeLogRecord`).

**Dashboard (`internal/cli/ui/dashboard`).** Eight tabs (status, karts, circuits, chest, characters, tunes, ports, logs). Tabular panels render through `bubbles/v2/table`; footer through `bubbles/v2/help` driven by a `keyMap` that interleaves the active panel's contextual keys with the dashboard globals. Status panel has the animated banner: hardcoded 3-row "drift" wordmark, rainbow gradient via `lipgloss.Blend1D`, harmonica spring (6.0 / 0.5) sliding banner / lockup / stats / activity into place over ~600ms with per-element delays. Opt-out via `--no-motion` / `DRIFT_NO_MOTION` / disabled theme / narrow terminal / `GO_TEST_DETERMINISTIC=1`.

**Wiring.** Bare `drift` on a TTY opens the dashboard. `drift menu` keeps the launcher picker. `drift dashboard --tab <name>` deep-links into a panel. `drift dashboard --demo` (also `DRIFT_DEMO=1`) renders against `internal/demo/fixtures.go` instead of live RPCs. `runDashboard` clears `DRIFT_DEBUG` for the program's lifetime so the SSH RPC transport's `MirrorStderr=os.Stderr` mirror doesn't scrawl `→ devpod list ...` over the alt-screen.

**Validation.** `internal/cli/ui/dashboard/snapshot_test.go` drives the model through `Init` + `WindowSizeMsg`, captures `View().Content`, writes to `testdata/snapshots/{empty,fixture}-*.txt`. Run `go test ./internal/cli/ui/dashboard/... -update` after intentional UI changes; without `-update` byte equality is the regression gate. The harness skips `tea.Tick` cmds with a per-call timeout so deterministic frames don't depend on wall clock. `mode_test.go` covers `DetectMode` matrix + NO_COLOR regression.

## Still to do

**Dashboard polish.**
- Karts panel lifecycle actions: confirmation-modal + RPC dispatch for `s` start, `x` stop, `R` restart, `B` rebuild, `C` recreate, `D` delete, `c` connect (execs out), `e`/`d` enable/disable autostart. Connect needs to release the alt-screen via `tea.ReleaseTerminal` before exec, then quit; lifecycle calls fire RPC and refresh the table.
- Karts panel row expand on `enter` (inline `drift kart info` key/value block), filter on `/`, sort cycle.
- Circuits panel `a` to add (inline form), `d` to delete, `space` to set default, `r` to rename, `enter` to drill into per-circuit detail. Zero-circuit autopops the add modal (subsumes `drift init`'s "first circuit" path).
- Chest / characters / tunes: `enter` expands to detail view (resolver backend / dotfiles / devcontainer fragment). Authoring stays in `lakitu …` over ssh per the client/server boundary rule in CLAUDE.md.
- Ports panel: real data layer. Plan 13 has shipped — wire `drift ports list` data into `liveDataSource.Ports`, add inline `a` / `d` flow.
- Logs panel: bubbles `viewport` for the tail, kart picker on top, `/` filter, `L` level cycle, `f` follow toggle, `s` save current contents. Reuse the `charmlog` formatter the one-shot `drift logs` already routes through.
- Cross-tab affordances: `:` opens a fuzzy command palette, `?` opens a full-help modal, toast region bottom-right for transient messages.
- Refresh-pause on terminal blur (bubbletea v2 emits focus events; verify Termux passes them through).

**`drift new` wizard.** Bare `drift new` on a TTY currently still errors. Ship the seven-step `huh.Form` from the plan: name (with kart-name regex validation), source pick (clone / starter / local) + URL input, circuit, tune, character, optional advanced (devcontainer / dotfiles / mounts / autostart / features), review + confirm, then drop into the same Color spinner+phases path the flagged invocation uses.

**Testing.**
- `teatest`-style scripted-flow tests for the dashboard happy paths (tab cycle, karts filter+restart, chest expand). Currently we have first-frame snapshots only.
- Component-level panel tests with mocked RPC clients to exercise error-state rendering, refresh-after-tick behavior, and the `Panel` interface invariants (every binding has non-empty Help, View width never overflows program width, WindowSizeMsg never panics).
- Fake-clock injection for tickers so the 10s status refresh can fire immediately under test.

**Demo / docs tooling.** `vhs` script (`docs/dashboard.tape`) driving the `--demo` dashboard, `make readme-gif` target to regenerate `docs/dashboard.gif`, README reference, teatest replay test that mirrors the tape's key sequence (the actual regression gate; the GIF is a docs asset). `make eval-frames` driving the demo dashboard through `freeze` to dump stills under `docs/eval/`, plus `docs/eval/rubric.md` so a developer iterating on UI polish can ask Claude Code to read frames and the rubric directly. Both need `vhs` and `freeze` added to the dev shell (`flake.nix`).

**Per-command redesigns from the plan's table.** Most of these compile-and-render but still reach for `huh.Form` / `huh.Confirm` directly rather than the `ui.Confirm` / `ui.Select` / `ui.Input` wrappers. Drive-by migration as we touch each command. `drift update` still has the hand-rolled `\r`-rewrite progress writer at `update.go:297`; replace with `ui.NewProgress` + `ui.PhaseTracker` for the surrounding stages.

**Glamour-rendered `drift help <topic>`.** Plan calls this an optional follow-up; not started.

## Known issues / open questions

- Active tab in the tab bar renders with bold + underline + (when enabled) accent foreground via lipgloss; per-grapheme styling produces verbose ANSI output (`[1;4;4ms[m[1;4;4mt[m...`). Functionally correct; cosmetic in `git diff` and snapshot inspection.
- Activity-table fade is a binary dim/normal switch at opacity 0.5 rather than a true alpha blend. lipgloss has `Alpha`/`Lighten` helpers; revisit if the threshold pop is visually distracting.
- Ports tab returns an empty slice from `liveDataSource.Ports` — placeholder until plan 13's data layer is wired into the dashboard.
- Snapshot harness uses `GO_TEST_DETERMINISTIC=1` to skip the entrance animation. Renaming this to `DRIFT_NO_MOTION` (already supported) would consolidate to one env var, but `DRIFT_NO_MOTION` reads as a user-facing toggle and a test-internal gate is clearer.
- `helpStylesFor` zeros out every `help.Styles` field when the theme is disabled. If `bubbles/v2/help` adds new style fields they'll silently retain defaults; revisit with a named constructor or a registry-driven loop if it bites.
