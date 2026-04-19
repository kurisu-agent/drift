# CLI prettification

Status: proposed
Owner: unassigned
Related: `internal/cli/drift/drift.go`, `internal/cli/drift/list.go`, `internal/cli/drift/circuit.go`, `internal/cli/drift/kart.go`, `internal/cli/drift/new.go`, `internal/cli/drift/warmup.go`, `internal/cli/errfmt`, `internal/slogfmt`, `internal/connect/connect.go`, `internal/exec/exec.go`, `internal/rpcerr/rpcerr.go`, `internal/kart/new.go`, `internal/server/kart_lifecycle.go`

## Problem

Drift's CLI output is stdlib-only: `fmt` + `text/tabwriter` + `slog`. Tables
are aligned but flat; errors, lifecycle summaries, and log levels have no
emphasis; the interactive warmup wizard has no visual identity. The JSON
path is clean and should stay that way — styling only needs to apply when
`root.Output == "text"` (see `internal/cli/drift/drift.go:19`).

## Goals

1. One shared styling grammar across tables, errors, and logs.
2. Colors + emphasis on the text path; zero change on the JSON path.
3. Automatic no-op when stdout isn't a TTY or `NO_COLOR` is set.
4. Small dependency footprint — prefer composable primitives over frameworks.

## Non-goals

- Full TUI / bubbletea screens. Drift is a one-shot command CLI.
- Progress bars for byte streams. Operations here are discrete steps.
- Restyling `--output json`. It stays machine-parseable.

## Libraries

Core styling: [`charmbracelet/lipgloss`](https://github.com/charmbracelet/lipgloss)
— declarative, composable, includes
[`lipgloss/table`](https://pkg.go.dev/github.com/charmbracelet/lipgloss/table)
with per-cell styling, built on
[`muesli/termenv`](https://github.com/muesli/termenv) so color-profile
detection + `NO_COLOR` + non-TTY handling is automatic.

Supplements:
- [`briandowns/spinner`](https://github.com/briandowns/spinner) — cheap,
  stdlib-adjacent spinner for `start` / `stop` / `connect` / `warmup`.
- [`common-nighthawk/go-figure`](https://github.com/common-nighthawk/go-figure)
  — figlet banner for the warmup wizard's first screen.
- [`mattn/go-isatty`](https://github.com/mattn/go-isatty) — gate styling on
  TTY detection.

Rejected: `pterm/pterm` (too opinionated, ~15 deps — drift already owns its
output layer and just needs a styling primitive, not a framework).
`schollz/progressbar` (operations are discrete steps, not byte streams).
`bubbles/spinner` (drags in the bubbletea runtime).

## Surfaces

- **Tables** — `list.go:58-79`, `circuit.go:185-195`
- **Lifecycle summaries** — `kart.go:34-48`, `new.go:78-89`
- **Errors** — `errfmt/errfmt.go:22-44` (single choke point)
- **Logs** — `slogfmt/slogfmt.go:71-96` (level coloring)
- **Warmup wizard** — `warmup.go:24-81` (banner + bordered step summaries)

## Plan

### Step 1 — `internal/cli/style/` package

Central palette + helpers. Exported `Style` functions short-circuit to a
no-op when any of these hold:

- `root.Output == "json"`
- stdout is not a TTY (`go-isatty`)
- `NO_COLOR` env var is set (termenv handles this already; assert it)

Palette:

```
success  green
warn     yellow
error    red
dim      gray      // secondary detail, timestamps, cause chain
accent   cyan      // identifiers, kart names
bold               // table headers, section titles
```

### Step 2 — rewrite `errfmt.go` + surface real devpod errors

Biggest single ROI, one file. Red `error:` prefix, dim cause chain, accent
on attribute keys. Wire through the style package so JSON mode stays
unaffected.

Same pass, fix the swallowed-stderr problem: `internal/exec/exec.go:48`
renders only `FirstStderrLine`, so a devpod failure whose first stderr
line is `warn Resolving dependencies tunnelserver.go:423` reaches the user
as exactly that — the real cause sits unread in `*exec.Error.Stderr`.

- In the server-side wrap sites (`internal/kart/new.go:160` and
  `internal/server/kart_lifecycle.go:73,88,103,107,138`), unwrap
  `*exec.Error` and attach the trailing ~20 stderr lines via
  `.With("devpod_stderr", …)` so they ride through `rpcerr.Data` to the
  client.
- In `errfmt`, render `Data["devpod_stderr"]` as a dim, indented block
  under the error message when present. Trim ANSI from devpod's own
  output before re-emitting (devpod uses charmlog colors).
- Cap the captured tail (~4 KB) so a runaway stderr can't bloat RPC
  payloads. Redact obvious secrets in stderr (`Authorization: …`,
  `token=…`) before attaching — devpod's own logs occasionally echo URLs
  with embedded credentials.

### Step 3 — convert tables to `lipgloss/table`

`list.go` and `circuit.go` swap `tabwriter` one-for-one. Header row bold;
state column colored (`running` green, `stopped` dim, `stale` yellow);
kart names in accent.

### Step 4 — `slogfmt` level colors

`DEBUG` dim, `INFO` default, `WARN` yellow, `ERROR` red. Timestamp stays
dim. Applies to the rendered `drift logs` output only; the JSONL wire
format is untouched.

### Step 5 — warmup banner

One-time `go-figure` "drift" banner at wizard start, then `lipgloss`
bordered panels for each step summary in `warmup.go`. Banner prints once
per wizard invocation, not per step.

### Step 6 — spinners + transport hint on long ops

Wrap the remote SSH call sites in `start`, `stop`, `connect`, `new` with
`briandowns/spinner`. Hidden under `--output json` or non-TTY. Message
reflects the phase (`"connecting to host…"`, `"starting kart…"`,
`"creating kart \"test3\"…"`).

Include a transport hint in the spinner suffix so the user can tell
which channel they're on. `internal/connect/connect.go:58` already
decides between mosh and ssh via `moshAvailable`; lift that decision
into a small `connect.Transport()` helper that returns `"mosh"` /
`"ssh"` and have `connect`/`ai` print it once at session start
(`via mosh` in dim style). For the RPC path
(`internal/rpc/client/client.go:121` `SSHTransport`), the channel is
always plain ssh — render it the same way for consistency
(`via ssh` next to the spinner).

### Step 7 — progress events on slow RPCs

`drift new` blocks for the entire server-side `devpod up` (minutes)
with no output. Same for `start`, `stop`, `restart`, `delete` — the
RPC is one round-trip, but the work is long. Add lightweight
client-side events around the spinner from Step 6:

- A **start line** before `rpcc.Call` in
  `internal/cli/drift/new.go:66` and the lifecycle sites in
  `internal/cli/drift/kart.go`: `creating kart "test3" from
  github.com/kurisu-agent/tzone-buddy via ssh…` (accent on the kart
  name, dim on the source + transport).
- A **completion line** on success that mirrors the existing
  `created kart "test3"` summary, but driven by the spinner's
  `FinalMSG` so it replaces the spinner cleanly.
- A **failure line** on RPC error: red `failed` marker, then defer to
  `errfmt` (which now carries the devpod stderr from Step 2).
- A **timer suffix** on the spinner past 10s
  (`creating kart "test3" via ssh… 0:42`) so users can tell the
  difference between "stuck" and "still working." `briandowns/spinner`
  exposes `Suffix` for this.

All four events route through the same style helpers as Step 1 and
no-op under `--output json` / non-TTY.

## Watch-outs

- All styling must no-op under `--output json`, non-TTY stdout, and
  `NO_COLOR`. CI logs and piped usage must stay clean.
- `drift logs` piped to `grep`/`less` must not emit ANSI by default. The
  TTY check on stdout handles this.
- Budget: lipgloss + spinner + go-figure + go-isatty adds ~5 direct deps.
  Acceptable; pterm alone would add ~15.
- Banner only in `warmup`. Don't sprinkle ASCII art elsewhere.
- The captured devpod stderr (Step 2) can echo URLs with embedded
  credentials or a `$HOME` path that contains the username. Redact
  before attaching to `rpcerr.Data`, and remember `Data` is serialized
  in `--output json` too.
- Spinner output goes to stderr, not stdout — stdout stays reserved for
  the structured summary so `drift new … | jq` keeps working when the
  user forgets `--output json`.
- The mosh/ssh hint should not block the operation if `mosh`
  detection itself errors; treat detection failure as `"ssh"` and move
  on (matches the existing fallback in `connect.go:58`).

## Sources

- [charmbracelet/lipgloss](https://github.com/charmbracelet/lipgloss)
- [lipgloss/table](https://pkg.go.dev/github.com/charmbracelet/lipgloss/table)
- [pterm/pterm](https://github.com/pterm/pterm) (rejected)
- [briandowns/spinner](https://github.com/briandowns/spinner)
- [common-nighthawk/go-figure](https://github.com/common-nighthawk/go-figure)
- [muesli/termenv](https://github.com/muesli/termenv)
- [mattn/go-isatty](https://github.com/mattn/go-isatty)
