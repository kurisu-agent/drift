# Nicer logs

Status: proposed
Owner: unassigned
Related: `internal/cli/drift/logs.go`, `internal/cli/errfmt`, `internal/wire`, `internal/server/kart_lifecycle.go`

## Problem

Drift has no structured logging. The server side writes ad-hoc strings, the CLI
renders `kart.logs` as one opaque `chunk` with a single wall-clock timestamp
slapped on every line regardless of when it was actually emitted
(`internal/cli/drift/logs.go:46`). The `--debug` / `DRIFT_DEBUG` flags are
parsed but never consulted. Users debugging a stuck kart get a wall of text
with no levels, no correlation, and no per-line time.

Meanwhile the error path is already good: `rpcerr.Error` + `errfmt.Emit`
gives a single human-readable renderer that sorts attributes and indents
cleanly. Logs should match that visual grammar, not diverge from it.

## Goals

1. Per-line timestamps, levels, and attributes when rendering `drift logs`.
2. One visual grammar shared between logs and errors.
3. Zero changes to the one-shot SSH / JSON-RPC transport shape.
4. `--output json` keeps working and emits something downstream tools can
   consume directly (JSONL).
5. Wire the dead `--debug` flag so it actually does something.

## Non-goals

- Live tail / streaming. Deferred — see "Future: streaming" below for the
  recommended approach and what it would cost. Not part of this phase.
- OpenTelemetry / `otelslog`. There is only one server process; there is
  nothing to correlate against. Revisit when a second service appears.
- Replacing Zap/Zerolog evaluations. `log/slog` (stdlib) is the default.

## Design

### 1. Wire format change: `kart.logs`

Today (`internal/server/kart_lifecycle.go:46`):

```go
type KartLogsResult struct {
    Name  string `json:"name"`
    Chunk string `json:"chunk"`
}
```

Change to JSONL-per-line:

```go
type KartLogsResult struct {
    Name   string   `json:"name"`
    Format string   `json:"format"` // "jsonl" | "text"
    Lines  []string `json:"lines"`
}
```

- `Format: "jsonl"` — each line is a JSON object shaped like a `slog` record
  (`time`, `level`, `msg`, plus arbitrary attrs).
- `Format: "text"` — each line is a raw string. Used as a fallback when the
  source (e.g. `devpod logs` on a process that doesn't log JSON) isn't
  structured. The CLI wraps these into synthetic `INFO` records at render
  time using the line's receipt time.

The server decides which format to emit based on whether it can parse the
upstream output as slog-JSON. No runtime negotiation needed.

### 1a. Params on `kart.logs`

Today `kart.logs` takes only `{name}`. Extend the params struct so the CLI
can express the obvious filters users expect from anything called `logs`:

```go
type KartLogsParams struct {
    Name  string        `json:"name"`
    Tail  int           `json:"tail,omitempty"`  // last N lines; 0 = server default
    Since time.Duration `json:"since,omitempty"` // e.g. 10m, 1h
    Level string        `json:"level,omitempty"` // min level; JSONL only
    Grep  string        `json:"grep,omitempty"`  // substring on msg
}
```

CLI surface:

```
drift logs foo -n 100
drift logs foo --since 10m --level warn
drift logs foo --grep "kart started"
```

Server-side implementation:

- `tail` / `since` — push down to the log source where possible
  (`devpod logs --tail N --since 10m`, `journalctl --lines N --since`).
  Fallback: slice the captured lines in the handler.
- `level` — only meaningful once the server is emitting JSONL records
  (see step 4). Filter by parsing each record's `level` field before it
  goes into the response. For `Format: "text"` sources, ignore the flag
  and note it in the rendered output.
- `grep` — substring match on `msg` for JSONL, on the raw line for text.
  Keep it substring-only for now; regex is YAGNI until someone asks.

Server default for `Tail` when unset: cap at some sane ceiling (1000?) to
avoid unbounded responses over the one-shot SSH channel. The ceiling is a
server-side constant, not a wire field — users who want more can page by
`--since` or run with `-n N` explicitly.

None of this requires a transport change; all params ride inside the
existing JSON-RPC request envelope.

### 2. New package: `internal/cli/slogfmt`

Mirrors `internal/cli/errfmt` exactly in spirit:

```go
package slogfmt

// Emit renders a single decoded slog record to w with drift's standard
// grammar: `HH:MM:SS LEVEL msg` on the header line, sorted `key: value`
// attributes indented beneath.
func Emit(w io.Writer, rec Record) { ... }

type Record struct {
    Time  time.Time
    Level string
    Msg   string
    Attrs map[string]any
}
```

Rendering rules (borrowed from `errfmt.Emit`):

- Header: `15:04:05 INFO  <msg>` — level padded to 5 chars, ANSI color on
  TTY only (respect `NO_COLOR`, reuse whatever `errfmt` does).
- Attributes: sorted by key, one per line, indented 2 spaces, `key: value`.
- Multi-line values: indent continuation lines to align under the value
  column.
- `error` attribute gets the existing `errfmt` chain-rendering treatment so
  the two paths produce identical-looking error blocks.

### 3. CLI render loop (`internal/cli/drift/logs.go`)

```go
switch result.Format {
case "jsonl":
    for _, line := range result.Lines {
        var rec slogfmt.Record
        if err := json.Unmarshal([]byte(line), &rec); err != nil {
            // bad line — fall back to a synthetic text record
            rec = slogfmt.Record{Time: now(), Level: "INFO", Msg: line}
        }
        slogfmt.Emit(io.Stdout, rec)
    }
case "text":
    for _, line := range result.Lines {
        slogfmt.Emit(io.Stdout, slogfmt.Record{
            Time: now(), Level: "INFO", Msg: line,
        })
    }
}
```

`--output json` mode: emit the response verbatim (already the behavior).
Downstream gets clean JSONL via `jq '.lines[] | fromjson'`.

### 4. Server-side log capture

For karts whose workload already emits slog-JSON, the server reads stdout
line-by-line, confirms each line parses as JSON with a `time` field, and
passes through. For everything else (`devpod logs`, legacy tools), emit
`Format: "text"`. No wrapping, no attempts at regex heuristics — keep it
binary: structured or not.

### 5. `--debug` flag

Currently parsed in `internal/cli/drift/drift.go:16` and
`internal/cli/lakitu/lakitu.go:30` but never read. Wire it to:

- Server: set the handler's minimum level to `slog.LevelDebug`. Kart
  workloads running under the server inherit this through env
  (`DRIFT_LOG_LEVEL=debug`) so their own slog handlers pick it up.
- CLI: `slogfmt.Emit` filters out records below the configured level. Lets
  a user run `drift --debug logs foo` to see debug records that the server
  captured but would otherwise hide.

If wiring turns out to be non-trivial, delete the flag instead of leaving
it lying. A parsed-but-ignored flag is a correctness bug.

### 6. Bonus: enrich RPC errors with recent records

`rpcerr.Error.Data` is an open `map[string]any` that `errfmt` already
pretty-prints. When a server handler fails, attach the last N slog records
captured during that handler call as `Data["recent_logs"]` (slice of
JSONL strings). `errfmt` + `slogfmt` together render them under the error
header. One round-trip, same envelope, free post-mortem.

This is where the one-shot transport pays off: we can't tail a live stream
over the single response channel, but we can bundle breadcrumbs into the
response itself.

## Migration plan

1. Land `internal/cli/slogfmt` with tests. No call sites yet.
2. Change `wire.KartLogsResult` + server handler to emit the new shape.
   Since every drift/lakitu pair is deployed together over SSH, there is
   no mixed-version concern — do the cut in one PR.
3. Rewrite `internal/cli/drift/logs.go` to use `slogfmt.Emit`. Delete the
   old whole-chunk timestamp logic.
4. Wire `--debug` to `slog.LevelDebug` server-side and to `slogfmt`'s
   level filter CLI-side. Or delete the flag.
5. (Separate PR) Teach handlers to tee slog records into a ring buffer
   scoped to the request context, and flush on error into
   `rpcerr.Error.Data["recent_logs"]`.

Steps 1–4 are one reviewable diff. Step 5 is optional polish and can wait
until the first time someone wishes they had it.

## Future: streaming (deferred)

Not in this phase. Documented here so the design choices made above don't
quietly foreclose it, and so a future reader doesn't have to re-derive the
tradeoffs.

### The hard constraint

The drift↔lakitu transport today is one `ssh drift.<circuit> lakitu rpc`
exec per invocation: one JSON-RPC request on stdin, one response on
stdout, process exits. Streaming breaks the one-response invariant. Any
design has to decide where the break happens.

### Three options

**A. NDJSON inside the RPC channel.** Reuse JSON-RPC notifications
(id-less requests) as server-pushed frames, terminate with the real
response. Pros: no new transport. Cons: every client decoder becomes
stream-aware; the "one request, one response" mental model is gone;
error shapes get muddled with partial-progress shapes. **Not
recommended.**

**B. Dedicated streaming method, same SSH exec, raw JSONL pipe.** A
method like `kart.tail` whose handler, after reading the request, stops
emitting RPC responses and pipes JSONL frames until the client
disconnects. Dispatch branches at the registry: normal methods return
`(any, error)`, streaming methods take `(io.Writer, context.Context)`.
Pros: one binary, one SSH path. Cons: two handler shapes; `lakitu rpc`
no longer has a clean read-one / write-one / exit loop.

**C. Separate SSH exec: `lakitu tail`.** Recommended. Keep `lakitu rpc`
pristine and single-shot. Streaming becomes a different lakitu
subcommand that `drift logs -f` exec's directly. Args come in as CLI
flags on `lakitu tail`, frames go out as raw JSONL on stdout. Pros:
protocol stays boring; streaming is a long-lived pipe and nothing more;
the RPC framework never learns about streams. Cons: `drift` grows a
second SSH-exec codepath — but it's a small one.

### Why C

- SSH already gives everything streaming needs: backpressure (TCP),
  cancellation (client Ctrl-C → SIGHUP → server exits), auth, the same
  `drift.<circuit>` destination.
- RPC stays boring. Boring is good at protocol boundaries.
- Client-side, `drift logs -f` becomes: exec SSH, read line-by-line,
  `json.Unmarshal` each into a `slogfmt.Record`, `slogfmt.Emit`, loop.
  Roughly 40 lines.
- `--output json` in follow mode is passthrough — same as non-follow.

### What streaming forces either way

1. **Cancellation semantics.** Rely on SIGHUP / SIGPIPE when the client
   goes away. Server handler needs a `select { case <-ctx.Done(): }`
   loop around its log reader.
2. **Followable log sources.** `devpod logs --follow`,
   `journalctl -f -u kart-foo`. If the upstream source buffers, followers
   see jitter — not fixable at this layer.
3. **No resume.** Drop connection, rerun. Don't build replay. If it ever
   matters, `--since $last_seen_time` covers it.
4. **Mixed JSONL + text sources.** Same wrapping story as the
   non-streaming case — either passthrough parsed JSON or synthesize a
   text record line-by-line.
5. **Concurrent followers.** Each SSH exec is independent. No subscriber
   registry, no fanout, no shared state.

### What streaming does not force

- No websockets, no gRPC, no long-lived RPC connection pool.
- No protocol versioning — `lakitu tail` is a new subcommand, old
  clients never see it.
- No error-handling changes — streaming errors become JSONL records with
  `level: "error"`, the stream ends, done.

### Prerequisites from this phase

The design above already covers the things streaming will need:

- `slogfmt.Emit` renders one record at a time — perfect for tailing.
- JSONL-per-line wire shape is already the non-streaming format, so the
  on-the-wire frame type is shared.
- `--level` filter logic lives in one place and can be reused
  server-side for `lakitu tail`.

So picking C later is cheap. The main work when the time comes is the
`lakitu tail` subcommand, the `drift logs -f` flag, and the Ctrl-C
plumbing. Everything else is already in place.

## What we explicitly reject

- **Streaming over the RPC channel.** Breaks the one-response-per-request
  invariant.
- **Zap / Zerolog.** No throughput problem; stdlib `slog` ergonomics and
  stability win.
- **`otelslog` today.** Premature without a second service.
- **A second renderer.** One visual grammar via `errfmt` + `slogfmt`, not
  two competing ones.

## Open questions

- Do we want color on by default when stdout is a TTY, or opt-in via a
  flag? `errfmt` sets the precedent — follow it.
- Should `Format` live on the envelope or per-line? Per-envelope keeps the
  server implementation binary and the client decoder trivial; revisit if
  we ever want mixed output from a single call.
- Ring-buffer size for `recent_logs` breadcrumbs: 50? 200? Defer until
  step 5.
