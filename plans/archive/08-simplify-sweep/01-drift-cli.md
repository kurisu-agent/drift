# Simplify sweep — drift CLI surface

**Paths reviewed:** `cmd/drift/`, `internal/cli/drift/`
**Agent:** Opus 4.7 (1M context) — chunk 1 of 7

## Summary

- `drift new` and `drift connect` bypass `deps.call` by instantiating their own `client.New()`, which makes them un-fakeable in unit tests and breaks the injection contract every other subcommand obeys.
- Five callers of `writeTable` copy‑paste the same "accent the first column" cellStyler closure; a shared helper would delete ~25 LOC and remove the only reason list.go/status.go/run.go/circuit.go import `lipgloss` directly.
- `resolveCircuit` re-loads the client config from disk, and `runCircuitSetName` loads it again after — every circuit/kart verb pays 1–2 YAML parses per invocation even though one load would cover both needs.
- Two concepts are stringly-typed around "circuit not found": `circuit.go:411` uses `rpcerr.TypeKartNotFound` (the wrong Type), and `ssh_proxy.go:39` hard-codes the literal `"circuit_not_found"`. Neither matches the other and there is no `TypeCircuitNotFound` constant.
- Every JSON-emitting subcommand hand-rolls the `json.Marshal` + `Fprintln` + errfmt-on-error dance (7 copies); one `emitJSON(io, v)` helper would collapse them all.

## Findings

### F1. `runNew` and `runConnect` bypass `deps.call`, breaking test injection — high

- **Where:** `internal/cli/drift/new.go:51,69`; `internal/cli/drift/connect.go:35`
- **What:** Both handlers construct a fresh `client.New()` instead of routing the RPC through `deps.call` like every other command. `deps` exists exactly so tests can substitute a fake — `kart_test.go` fakes it to exercise start/stop/etc., but `new_test.go` can only test pure helpers (`expandOwnerRepoShorthand`, `shouldAutoConnect`) because the real RPC path is un-stubbable.
- **Why it matters:** The two most user-visible commands (`drift new`, `drift connect`) have zero coverage of their main code paths, including the name-collision retry loop. Regressions land silently.
- **Suggested fix:** Replace `rpcc.Call(ctx, circuit, …)` with `deps.call(ctx, circuit, …)`. `connect.go`'s case is slightly more work because `connect.Run` takes a `Call` func — plumb `deps.call` into `connect.Deps.Call` instead of `client.New().Call`. The `rpcc := client.New()` line disappears in both files.

### F2. Five copies of the same "accent column 0" cellStyler — med

- **Where:** `internal/cli/drift/circuit.go:231-238,470-476`; `list.go:82-99`; `run.go:61-67`; `status.go:139-157`
- **What:** Every table in the chunk builds a `func(row, col int, _ *style.Palette) lipgloss.Style` closure whose body is "if col == N return accent else default". Accent color is `lipgloss.Color("6")` literal, duplicated inline — the same value `style.go:41` already stores in `Palette.accent`.
- **Why it matters:** `list.go`, `run.go`, `status.go`, `circuit.go` all pull in `github.com/charmbracelet/lipgloss` solely to rebuild the accent style the palette already owns; the palette's color choice and the inline `Color("6")` can drift apart.
- **Suggested fix:** Add `table.go`-local helpers:
  ```go
  func accentCol(col int) tableCellStyler { ... }         // covers 4 of 5 call sites
  func styleFromPalette(p *style.Palette) { ... }          // expose p.accent as a lipgloss.Style
  ```
  Or take the column index directly: `writeTableAccentCol(w, p, headers, rows, 0)`. After this, list/run/circuit no longer import `lipgloss`.

### F3. `resolveCircuit` + caller both load the client config from disk — med

- **Where:** `internal/cli/drift/kart.go:14-30`; double-load in `circuit.go:300-344` (resolveCircuit then LoadClient at 324); same pattern any time a command needs both the circuit name and the `cfg` object.
- **What:** `resolveCircuit` opens + parses `~/.config/drift/config.yaml`, returns only the name, throws the `*config.Client` away. `runCircuitSetName`, `runCircuitSetDefault`, `runStatus`, `runSSHProxy` then reload the exact same file.
- **Why it matters:** Two YAML parses per command on the hot path; also invites drift between the "name" resolved first and the map loaded second if anything mutates the file mid-command (not realistic, but the shape is wrong).
- **Suggested fix:** Return `(*config.Client, string, error)` from `resolveCircuit` (or split into `loadClientConfig` + `pickCircuit`). Callers that only need the name drop the config return with `_`.

### F4. `TypeCircuitNotFound` doesn't exist; two different errors represent it — med

- **Where:** `internal/cli/drift/circuit.go:411` uses `rpcerr.TypeKartNotFound` for a missing circuit; `ssh_proxy.go:38-43` hard-codes the literal `"circuit_not_found"` string where a `Type` constant is expected.
- **What:** The rpcerr type registry (`internal/rpcerr/rpcerr.go:44-61`) has `TypeKartNotFound`, `TypeCharacterNotFound`, `TypeChestEntryNotFound` — but no `TypeCircuitNotFound`. `circuit.go` papered over that by reusing the kart type; `ssh_proxy.go` papered over it by stringly-typing the name nobody registered.
- **Why it matters:** Clients parsing `error.type` for programmatic branching see `kart_not_found` from `circuit set default nonexistent`, and a bespoke `circuit_not_found` from the SSH proxy — inconsistent contract. Also obscures the real gap (no registered type).
- **Suggested fix:** Add `TypeCircuitNotFound Type = "circuit_not_found"` to `internal/rpcerr/rpcerr.go`, then use it in both call sites. **Cross-ref:** chunk 5 (rpcerr registry).

### F5. Seven copies of `buf, err := json.Marshal(…); Fprintln; return errfmt.Emit` — med

- **Where:** `circuit.go:210-215,258-263,286-290,366-375,423-431`; `run.go:43-49`; `status.go:75-82`; `new.go:95-101`; `drift.go:204-210` (version). At least nine call sites total.
- **What:** Every `--output json` branch hand-rolls the same 6-line sequence.
- **Why it matters:** Noise in every command; single-line inconsistencies creep in (some use `MarshalIndent` for info but `Marshal` for list — decidable from one flag).
- **Suggested fix:** Add to `table.go` (or a new `emit.go`):
  ```go
  func emitJSON(io IO, v any) int {
      buf, err := json.Marshal(v)
      if err != nil { return errfmt.Emit(io.Stderr, err) }
      fmt.Fprintln(io.Stdout, string(buf))
      return 0
  }
  ```
  Callers drop to `return emitJSON(io, payload)`.

### F6. `pickCircuitDefault` and `promptNewKartName` hand-roll bufio prompting while the rest of the CLI uses `huh` — med

- **Where:** `internal/cli/drift/circuit.go:441-493` (bufio), `new.go:217-227` (bufio), `delete.go:41-58` (bufio) vs. `migrate.go:119-140,172-197,275-294` (all huh), `menu.go:53-108` (huh).
- **What:** Three prompts use `bufio.NewReader(io.Stdin).ReadString('\n')` with hand-written number parsing, ANSI on stderr, and their own `defaultRow := -1; _ = defaultRow` dead assignment (circuit.go:455,469). The other four use `huh.NewSelect`/`huh.NewInput` with built-in filtering and abort handling.
- **Why it matters:** Two UX flavors for the same operation; the bufio path in `pickCircuitDefault` actively contains dead code (`_ = defaultRow`). Users see one form for `circuit set default`, a completely different one in `migrate` and `menu`.
- **Suggested fix:** Port `pickCircuitDefault` and `promptNewKartName` to `huh.NewSelect`/`huh.NewInput` to match migrate.go/menu.go. Delete the `defaultRow := -1` / `_ = defaultRow` pair entirely (circuit.go:455,460,469). `delete.go`'s y/N can stay bufio since it's a single keypress.

### F7. `containsString` duplicates `slices.Contains` — low (but batchable with F8–F10 below)

- **Where:** `internal/cli/drift/migrate.go:313-320`; only caller `migrate.go:181`; tests in `migrate_test.go:34-40`.
- **What:** Go 1.26 (per `go.mod:3`) ships `slices.Contains[S ~[]E, E comparable](s S, v E) bool`. `containsString` is a strict subset.
- **Why it matters:** 7 lines + a standalone test for a stdlib function.
- **Suggested fix:** `import "slices"`, replace call with `slices.Contains(options, pick)`. Delete `containsString` and its test.

### F8. `logsParams` is a byte-for-byte alias of `logsCmd` — low

- **Where:** `internal/cli/drift/logs.go:16-22` declares `logsParams` with the same fields/tags as `logsCmd` (lines 37-43); `logs.go:50` does `params := logsParams(cmd)` — a type cast.
- **What:** The mirror exists "to keep the CLI off a compile-time dep on internal/server" (comment line 15), but `logsCmd` itself carries JSON tags and is already wire-shaped. The cast is the only use.
- **Suggested fix:** Delete `logsParams`. Use `cmd` directly: `deps.call(ctx, circuit, wire.MethodKartLogs, cmd, &raw)`. The JSON tags are already present on `logsCmd`.

### F9. `or(a, fallback)` duplicates `cmp.Or` — low

- **Where:** `internal/cli/drift/migrate.go:322-327`, used once at line 206.
- **What:** Go 1.21+ has `cmp.Or[T comparable](vals ...T) T` (returns first non-zero). Same semantics.
- **Suggested fix:** `import "cmp"`, replace `or(tune, "(none)")` with `cmp.Or(tune, "(none)")`. Delete `or`.

### F10. `stdinIsTTY` and `stdoutIsTTY` are two copies of the same check — low

- **Where:** `internal/cli/drift/init.go:86-100`; `menu.go:111-123`.
- **What:** Identical bodies except one takes `r any`, the other `w any`.
- **Suggested fix:** One `isTTY(fd any) bool` helper in `init.go` (or move both to a new `internal/cli/drift/tty.go`). Both existing exports become one-liners that delegate, or callers switch directly.

### F11. `p.Enabled`-gated "transport hint" printing appears in 4 places — low

- **Where:** `run.go:87-94`; `run.go:162-165`; `run.go:168-171`; `connect.go:55-58`.
- **What:** Same pattern: `p := style.For(w, jsonMode); if p.Enabled { fmt.Fprintln(w, p.Dim(...)) }`. The `p.Dim` method already short-circuits when the palette is disabled, so the `if p.Enabled` guard is redundant for the message text; the only reason to keep the guard is to skip the whole `Fprintln` in non-TTY mode — but the caller has already decided the writer is stderr and should be happy to drop dim prefixes silently.
- **Suggested fix:** Drop the `if p.Enabled` guards; `fmt.Fprintln(w, p.Dim(msg))` is a no-op styling-wise and the string is pure informational flavor that piped callers won't see anyway (stderr). Alternatively, add `p.Println(w, s)` that internalizes the gate.

### F12. `helpCmd.Full` flag + curated/full duplication — low

- **Where:** `internal/cli/drift/help.go:14-103`; `driftHelpSections` (20-49) is manual, `writeDriftHelp` (59-84) re-implements its own column-width math while `clihelp.Render` already ships (clihelp/clihelp.go:28-75).
- **What:** The curated path duplicates formatting primitives the LLM-help package already provides (sections + bodies rendered verbatim). The hand-curated table's main value is grouping — that could be expressed as `clihelp.Section` bodies that are pre-formatted.
- **Suggested fix:** Render the curated help by feeding `driftHelpSections` into `clihelp.Render` as `Sections`. Delete `writeDriftHelp` and its column-width loop (help.go:59-84). Keep the --full toggle as "also include the auto-derived catalog".

### F13. Batched low-severity — single line

- `drift.go:23-27` `Version bool` field is documented as "never read", but Kong scans it anyway — could use `kong.ConfigureHelpCommand`-style registration instead. `drift.go:241-261 outputFromArgv` re-parses the same argv Kong already walked; acceptable since it runs before Kong, but document why it can't call `kong.Parse` defensively. `migrate.go:145-166` claims to "parallelize" the two list calls in its comment but actually runs them serially — the comment lies; either delete it or fulfil it with an `errgroup.Group`. `status.go:63-73` sequentially probes every configured circuit — with N circuits the status command runs in N × (RTT+SSH-startup), trivially parallelizable with `errgroup.Group` since the struct entries are independent. `update.go:96-109 resolveSelfPath` and `cmd/drift/main.go:65 isSelfPath` both deal with termux exec quirks but diverge in approach — pull the shared "is this actually drift's binary path" logic into `internal/exec`. `menu.go:75-81` linear scan over `menuEntries` to find the picked key; build a map once at package init. `circuit.go:495-504 sshManagerFor` always returns `Options{Manage: true}` and a nil error — simplify to a non-error-returning helper. `drift.go:128-179` the giant switch could be a map `cmd[string] -> handler`, but the ergonomic win is small since each case has distinct parameter shapes. `new.go:172 writeNewPreflight` takes a literal `interface{ Write([]byte) (int, error) }` instead of `io.Writer`; same in `start.go:52` `writeLifecyclePreflight`. Use `io.Writer`. `run.go:182-196 readLastScaffold` shells out via `ssh -T target 'if [ -s … ] then cat…'`; could instead reuse `deps.call` with a hypothetical `kart.scaffolder.tail` method — but that's a server-side change, leave as-is.

## Nothing to flag

- `cmd/drift/main.go` — the Termux exec-path workaround is documented, tested, and necessarily unique; `isSelfPath` correctly orders env-var check before inode compare.
- `internal/cli/drift/dnsfix.go` — small, well-commented, well-tested, correct try-first ordering for systemd-resolved.
- `internal/cli/drift/probe.go`, `deps.go` — minimal, idiomatic injection surface.
- `internal/cli/drift/table.go` — clean separation of styled vs plain path; only improvement is factoring the callers, not the helper itself.
- `internal/cli/drift/home.go` — 7-line package-level swappable helper; can't get smaller.
- `internal/cli/drift/update.go` download/tar extraction path — tight, bounded, correct; the only nit (shared termux-path logic with main.go) is noted in F13.
- `internal/cli/drift/init.go` — pure delegator into `internal/warmup`; no simplification leverage here (warmup lives in chunk 4).
- `internal/cli/drift/stop.go`, `restart.go`, `disable.go` — 15-line thin wrappers by design; the shared lifecycle helper (`start.go:25`) already extracts the common body.
