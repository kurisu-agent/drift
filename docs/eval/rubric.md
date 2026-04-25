# Drift dashboard visual rubric

This file is the contract for the plan-16 visual loop. The agent regenerates frames with `make eval-screens`, reads the PNGs alongside this rubric, and lists failures with severity (`blocker` / `polish` / `nit`) plus a suggested fix. Address blockers first; rebaseline snapshots when the rubric is clean for the panel.

The brand guidelines section of `plans/16-dashboard-rebrand.md` is the definitive style reference; this rubric checks whether a given frame *adheres* to those guidelines.

## How to run the loop

```
1. Edit code (one panel or one cross-cutting concern at a time).
2. make eval-screens
3. In a Claude Code session, run:
     Read docs/eval/rubric.md
     Read docs/eval/*.png
   Evaluate each frame against the rubric. List failures with severity.
4. Fix in order. Repeat from step 2.
5. When rubric-clean for a panel: rebaseline snapshots
     go test ./internal/cli/ui/dashboard/... -update
   then commit. Move to the next panel.
```

When a rubric check is ambiguous on a given frame, prefer the brand guidelines as the tie-breaker; if those are silent too, flag the case as an open question instead of guessing.

## Cross-cutting checks

Apply to every frame. Failure on any of these is a blocker.

- **Wordmark gradient.** Where the `drift` wordmark renders, the per-column rainbow gradient is even across all letterforms; no flat-color column, no banding mid-letter.
- **Outer dashboard border.** The dashboard surface is wrapped in a `lipgloss.RoundedBorder()` whose color is `theme.Border.Subtle`. The border is unbroken except where the active tab welds in.
- **Welded tab strip.** Active tab integrates into the body via the canonical border-cut pattern (active label sits on a tab cell whose bottom edge merges with the body's top edge; inactive tab cells preserve the body's top edge).
- **Active vs inactive tab color.** Active tab label uses `theme.Border.Focus`; inactive labels use `theme.Text.Muted`. Tab geometry (padding, label width) is identical between active and inactive — focus is communicated through color only.
- **Tab separators.** A small middle-dot or thin vertical glyph separates inactive tabs; the separator is `theme.Text.Subtle`-toned and never louder than the tab labels themselves.
- **Footer help line.** Footer renders via `bubbles/help` short mode, with key glyphs and middle-dot separators in framework defaults. No bespoke restyling. Help line color is `theme.Text.Muted` for descriptions, `theme.Text.Default` for keys, brand accent for the leading icon.
- **Status pills.** When status is rendered as a pill (karts status column, ports conflict column), the pill is `Bold(true).Padding(0,1)` with the status-color background and contrast-safe foreground. Pills read at a glance: an icon, one word, one color. Inline icon+label is fine for ambient row status; pills are reserved for scan-a-column moments.
- **Nerd Font glyphs.** No tofu boxes (□) anywhere; verify the Nerd Font is installed in the rendering environment. If a glyph renders as tofu, it is wrong (Nerd Font is assumed by plan 16; ASCII fallback is removed).
- **Right-aligned columns are right-aligned.** Numeric columns (counts, latencies, ports) align on the rightmost digit; mixed-width labels do not stagger the column.
- **Text level hierarchy is visible.** Dim, muted, and default text are visually distinct on the rendered background. Muted is darker than dim is darker than default — or the inverse for light mode — but the three should never collapse into one indistinguishable tone.
- **Panel chrome is consistent.** Borders, padding, and spacing within panels match across tabs. Two panels at the same tab depth should look like siblings, not strangers.
- **Visual hierarchy on the page.** One element per panel draws the eye first (banner, table header, focused row). The page communicates a clear primary, secondary, tertiary tier.
- **No raw ANSI leaks.** No `\x1b[...m` sequences appear as literal text inside the rendered frame.
- **No tab feels less finished.** Stand back from the screen and squint: every tab should read as part of the same product. If one tab looks like a prototype next to a polished sibling, that gap is a blocker.

## Per-page rubrics

Each section names the scenario PNG it applies to and lists checks specific to that surface. Cross-cutting checks above also apply.

### status (default)

`docs/eval/status-default.png`

- Banner is the leftmost element, vertically aligned with the first row of the lockup.
- Lockup right of the banner: line 1 is the version (`drift v0.x`), line 2 is the tagline, line 3 is the small hint about `?` for help. Vertical rhythm is even (no double-space between lines).
- Stats block is right-aligned to the panel; numbers are bold default-color, labels are muted, alignment is on the rightmost digit of each number.
- Activity table fills the remaining vertical space. Columns: time (muted), action (default), kart (accent), detail (muted). Separator between header and body is a thin rule.
- The eye lands on the wordmark first, then the stats block, then the most recent activity row.

### karts (default · filter-active · row-expanded)

`docs/eval/karts-default.png`

- Header row is bold, separated from the body by a thin rule.
- Status column is a pill (running → success bg; stopped → muted bg; stale → warn bg; error/unreachable → error bg). The pill is the most visually prominent column.
- Selected row is highlighted in `Border.Focus` background with high-contrast foreground; selection cursor is a chevron at the leftmost column.
- Empty state is one short sentence (`no karts yet, drift new to create one`), centered.

`docs/eval/karts-filter-active.png`

- A `/`-prefixed filter input replaces the footer or sits as a one-line strip directly above the table. Cursor is visible.
- Non-matching rows are rendered dim, not removed; matching rows render at default text level. Match count is shown somewhere unobtrusive ("3/9 match").
- Pressing esc clears the filter and returns the panel to default.

`docs/eval/karts-row-expanded.png`

- Expanded row reveals a sub-block beneath it (indented, bordered top, RoundedBorder corner glyphs), showing the kart's detail (image, last used, autostart, ports if any).
- The sub-block does not push the table off-screen; the table viewport scrolls if needed.
- A small `▾` glyph next to the expanded row indicates state.

### circuits (default · with-color-tint)

`docs/eval/circuits-default.png`

- Columns: name, host, default, lakitu, latency, state.
- Default circuit is marked with a small star icon in the leftmost column; not a separate column.
- State pill: reachable → success bg, unreachable → error bg.

`docs/eval/circuits-with-color-tint.png`

- Each row carries its per-circuit color tint as a small swatch (1-2 cells wide) next to the name.
- Swatches are visible at glance; circuits with no color use a muted neutral.
- When dashboard is anchored to a single circuit (filter active), the outer border `Border.Focus` takes the circuit color; cross-circuit views revert to the brand accent.

### chest, characters, tunes (default · row-expanded)

`docs/eval/chest-default.png` / `characters-default.png` / `tunes-default.png`

- Body has a one-line hint at the top: "authoring lives in lakitu — drift connect <circuit> -- ...". Hint is muted, single line, no border.
- Table columns: circuit, name, detail, used-by. Detail is truncated at column width with a `…` suffix.

`docs/eval/<panel>-row-expanded.png`

- Expanded row reveals the resource detail (resolver, git identity, devcontainer fragment respectively).
- Indentation, border, and chevron behavior match karts-row-expanded.

### ports (default · with-conflict)

`docs/eval/ports-default.png`

- Columns: local, remote, circuit, kart, state.
- Direction of forwarding is unambiguous: a `↦` (or `→`) glyph between local and remote columns, never just two numbers.
- State pill: forwarding → success bg, idle → muted bg.

`docs/eval/ports-with-conflict.png`

- Rows that conflict on host port (two forwards bound to the same local port) are rendered with a warning-pill state ("conflict") and an inline warning glyph at the leftmost column.
- Conflict color is warn, not error.

### logs (default · follow-on · filter-active)

`docs/eval/logs-default.png`

- Top: kart picker (one line, accent on the focused kart name, muted on the rest).
- Body: viewport with timestamps (muted, fixed-width column), level pills (info → accent bg; warn → warn bg; error → error bg), message (default).
- Right edge of the viewport shows a thin scrollbar track.

`docs/eval/logs-follow-on.png`

- Follow indicator is a small "● follow" badge in the top-right of the panel, brand accent. When follow is off, the badge reads "○ paused" in muted.

`docs/eval/logs-filter-active.png`

- Filter strip behaves like karts. Non-matches dim; match count visible.

### Cross-cut: palette · help · toasts · narrow

`docs/eval/cross-cut-palette-open.png`

- A centered modal over the body, RoundedBorder in `Border.Focus`. Width is ~60% of the viewport, capped at 80 cols.
- Top: an input prefixed with `:` and a cursor.
- Below: a vertical list of commands, scored by fuzzy match. Best match highlighted in `Border.Focus` background.
- Body behind the modal renders dimmed (a partial-opacity overlay simulated via foreground darkening).

`docs/eval/cross-cut-help-modal.png`

- Centered modal, same chrome as palette. Title row at the top: "drift help".
- Two-column key+description layout pulled from `bubbles/help` full mode.
- Last row is muted: "press ? or esc to close".

`docs/eval/cross-cut-toast-success.png` / `cross-cut-toast-error.png`

- Toast appears bottom-right of the body region (above the footer), rounded, padded 0/2.
- Success: `theme.Status.Success` border, success glyph + message in default color.
- Error: `theme.Status.Error` border, error glyph + message.
- 3s TTL; no fade animation in still frames, but the same chrome is used for both states.

`docs/eval/cross-cut-narrow-80c.png`

- 80-column rendering does not wrap the tab strip awkwardly. Either the welded strip stays single-line (with truncated labels if needed) or it falls back to numeric pips (`1·2·3·4·5·6·7·8`) with the active pip highlighted.
- Tables collapse the lowest-priority columns (last-used, source) to keep the highest-priority ones (name, status) at full width.

## Severity guidance

Use the following labels when listing failures so fixes can be triaged:

- **blocker** — violates a cross-cutting check, breaks a brand guideline, or makes the panel unusable. Fix before moving on.
- **polish** — a per-panel check fails or two panels disagree on a chrome detail. Fix in this pass; do not block on it if running out of time.
- **nit** — preference or micro-tweak that does not affect comprehension. Park as TODO.

## Updating this file

This rubric is a living document. As new panels, scenarios, or chrome elements land, append the relevant checks. Don't delete checks that pass — they are the regression bar. When a check no longer applies (the underlying decision was reversed), strike through the line with `~~...~~` and add a note explaining why.
