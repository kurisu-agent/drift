# Simplify sweep — lakitu CLI + shared CLI infra

**Paths reviewed:** `cmd/lakitu/`, `internal/cli/lakitu/`, `internal/cli/errfmt/`, `internal/cli/progress/`, `internal/cli/style/`, `internal/clihelp/`, `internal/cliscript/`
**Agent:** Opus 4.7 (1M context)

## Summary

- `lakitu`'s human subcommands rebuild the whole RPC registry on every call (including `EnsurePinned` ~117MB download logic and devpod binary resolution) just to dispatch `chest list` — hot path is O(full server boot) per invocation.
- `runInit` and `serverInitHandler` implement the same four-step garage-init sequence twice with divergent output vs. result shape — should share an inner helper returning `(InitResult, sideEffects)`.
- `CLI.Debug` (the Kong `--debug` / `LAKITU_DEBUG` flag) is parsed and then ignored; `Registry()` reads `os.Getenv("LAKITU_DEBUG")` directly. Either wire the parsed value through or drop the struct field.
- `lakitu.go`'s 26-case `switch kctx.Command()` (lakitu.go:75-120) is hand-rolled dispatch. Kong exposes `kctx.Run(...)` for method-based dispatch; drift has the same antipattern but this chunk only flags lakitu's copy.
- Minor shared-helper dead code: `style.Disabled()` has zero non-test callers, `style.StripANSI` is used by exactly one caller (`errfmt`), and `Palette.Success` is used by exactly one caller (`progress`).

## Findings

### F1. Registry() is rebuilt on every human subcommand — incl. devpod pinning — med

- **Where:** `internal/cli/lakitu/subcommands.go:205` (`Registry().Dispatch(...)` inside `callAndPrint`), `internal/cli/lakitu/lakitu.go:139-206` (`Registry()` body).
- **What:** `callAndPrint` calls `Registry()` per invocation. `Registry()` in turn calls `resolvePinnedDevpod` → `devpod.EnsurePinned` (hash compare against `<driftHome>/bin/devpod`, which on first run downloads a ~117MB release asset) and constructs two separate `devpod.Client` instances, registers all six kart services, etc. A `lakitu chest list` or `lakitu config show` pays that cost even though those RPCs never touch devpod.
- **Why it matters:** On a fresh circuit, running `lakitu chest list` before `lakitu init` triggers a 117MB download for a method that would resolve in µs. More importantly: the registry construction also emits a `"warning: pinned devpod unavailable (…)"` on stderr for every chest/config/character subcommand when devpod isn't reachable, polluting output of pure-config subcommands.
- **Suggested fix:** Split `Registry()` into `registerNonDevpod(reg)` (server, config, chest, character, tune handlers) and `registerDevpodBackedHandlers(reg)` (kart, kart.new, kart.lifecycle, kart.migrate, kart.autostart). Gate the devpod-backed registration on the method about to be dispatched, or cache a package-level `sync.OnceValue[*rpc.Registry]`. Tests can still swap via a setter.

### F2. runInit and serverInitHandler duplicate the same four-step init sequence — med

- **Where:** `internal/cli/lakitu/lakitu.go:208-251` (`runInit`) and `internal/cli/lakitu/lakitu.go:291-328` (`serverInitHandler`).
- **What:** Both call `config.GarageDir() → InitGarage → DriftHomeDir() → EnsureClaudeMD → EnsureRunsYAML → EnsureScaffolderRecipe`. They diverge on where the `"../CLAUDE.md"` / `"../runs.yaml"` / `"../recipes/scaffolder.md"` strings go (stdout lines vs. `res.Created` append) and on error handling (prefix `errfmt.Emit` vs. `rpcerr.Internal(...).Wrap(err)`).
- **Why it matters:** Adding a new Ensure* step means touching two places and keeping paths ("../CLAUDE.md" literals) in sync; drift between the two is silent.
- **Suggested fix:** Introduce `config.InitGarageFull(root, driftHome) (*InitResult, error)` that does all four steps and appends the relative virtual paths to `Created` itself. `runInit` then prints from the returned `Created`, `serverInitHandler` returns it directly. Removes the repeated `"../CLAUDE.md"` / `"../runs.yaml"` / `"../recipes/scaffolder.md"` literals.

### F3. CLI.Debug field is parsed but never read — low

- **Where:** `internal/cli/lakitu/lakitu.go:29` (`Debug bool `help:"Verbose output." env:"LAKITU_DEBUG"``), vs. `internal/cli/lakitu/lakitu.go:155` (`if os.Getenv("LAKITU_DEBUG") != "" { mirror = ... }`).
- **What:** Kong parses `--debug` / `LAKITU_DEBUG` into `cli.Debug` in `Run`, but `cli.Debug` is never referenced afterwards. `Registry()` reads the env var directly instead, which also means `lakitu --debug rpc` (flag form) silently does nothing — only the env form works.
- **Why it matters:** Mismatch between documented behaviour (`--help` lists `--debug`) and actual behaviour. A user passing `--debug` expects verbose output; they have to re-run with `LAKITU_DEBUG=1`.
- **Suggested fix:** Pass `cli.Debug` into `Registry(cli.Debug)` and use it (OR with `os.Getenv("LAKITU_DEBUG") != ""`) to gate `mirror`. Simpler alternative: drop the struct field and document only the env var.

### F4. 26-case hand-rolled switch over `kctx.Command()` — low

- **Where:** `internal/cli/lakitu/lakitu.go:75-120`.
- **What:** Every subcommand is routed through a giant switch keyed on Kong's slash-joined command path string (`"info <name>"`, `"kart new <name>"`, `"config show"`, …). Adding a command requires: declaring a struct, adding a case to the switch, wiring a `runXxx` function. Kong's own `kctx.Run(io, ctx)` mechanism dispatches directly to `Run` methods on the command structs.
- **Why it matters:** The switch is ~45 LOC of string matching that duplicates what Kong already knows. A typo in a string (`"info <name>"` vs `"info"`) silently falls through to `default → "unknown command"`. Drift has the same antipattern but this chunk is only scoped to lakitu.
- **Suggested fix:** Define `func (c *kartListCmd) Run(io IO, ctx context.Context) error` etc., bind via `kong.Bind(io, ctx)`, call `kctx.Run()`. Deletes the entire switch. Cross-ref: chunk 1 (drift CLI) has the same shape and could be migrated in the same PR.

### F5. GarageDir() error silently skips all kart handler registration — low

- **Where:** `internal/cli/lakitu/lakitu.go:143-144` (`garage, err := config.GarageDir(); if err == nil { ... }`).
- **What:** If `config.GarageDir()` fails, the if-block is skipped and `RegisterKart*` calls never run. A subsequent `lakitu rpc kart.new` returns `method_not_found` — misleading, since the method *is* defined, the server just couldn't find its state dir.
- **Why it matters:** Operator gets `"method "kart.new" not implemented"` when the real problem is a missing `$HOME`. The error is recoverable and worth surfacing.
- **Suggested fix:** Either `panic(err)` (unrecoverable startup) or have `Registry()` return `(*rpc.Registry, error)` and let callers `errfmt.Emit` + exit. In practice `config.GarageDir()` only fails on broken `$HOME`, so a startup-time hard error is fine.

### F6. Two identical devpod.Client constructions in Registry() — low

- **Where:** `internal/cli/lakitu/lakitu.go:182` and `internal/cli/lakitu/lakitu.go:193` — both `&devpod.Client{Binary: pinnedBin, Mirror: mirror, DevpodHome: driftDevpod}` with identical fields.
- **What:** One devpod client is attached to `kartDeps.Devpod`, a second fresh instance is attached to `kart.NewDeps{Devpod: ...}` passed to `server.RegisterKartNew`. The two clients share no state, so there's no reason to avoid reuse.
- **Why it matters:** Copy-paste duplication; if one ever drifts (e.g. adds a timeout field to one and not the other), debugging is harder.
- **Suggested fix:** Hoist `devpodClient := &devpod.Client{...}` once, pass the same pointer to both. Same note applies to `lifeDeps := &server.Deps{GarageDir: garage}` (line 180) vs. `Deps: &server.Deps{GarageDir: garage}` on line 190 — the former is already in scope.

### F7. style.Disabled() and style.StripANSI are thinly used helpers — low

- **Where:** `internal/cli/style/style.go:47` (`Disabled()`), `internal/cli/style/style.go:109-111` (`StripANSI`).
- **What:** `Disabled()` has zero non-test callers (only `style_test.go:34`). The `Palette.Success/Warn/Error/Dim/Accent/Bold` methods already no-op on nil or `Enabled:false`, so callers just use `&style.Palette{}` or `nil`. `StripANSI` has exactly one caller: `errfmt.writeDevpodTail`.
- **Why it matters:** `Disabled()` is a constructor nobody uses — dead API surface. `StripANSI` being a `style` export implies it's a general helper, but it's really an errfmt-only concern.
- **Suggested fix:** Delete `Disabled()`; replace the one test usage with `style.For(&bytes.Buffer{}, false)`. Consider moving `StripANSI` + `ansiRE` into `internal/cli/errfmt/` as an unexported helper, or leave it if other callers are anticipated.

### F8. errfmt.writeDevpodTail manually replicates indent logic — low

- **Where:** `internal/cli/errfmt/errfmt.go:75-84`.
- **What:** Hard-codes `"  "` (header indent) and `"    "` (line indent) as string prefixes concatenated before `p.Dim(...)`. The main error renderer at line 56-57 uses `"  "` for its indent. Multiple indent depths as string literals diverge silently.
- **Why it matters:** Low-severity, but a stringly-typed indent convention is the kind of thing future callers copy wrong. `errfmt` is shared infra so the leverage matters.
- **Suggested fix:** Introduce `const keyIndent = "  "; const blockIndent = "    "` at the top of the file, or better, a `writeIndented(w, p, depth int, label string)` helper. Drop the `p.Dim("  "+label)` string concatenation in favour of writing the prefix unstyled (terminal dim on spaces is invisible anyway).

### F9. Progress spinner's timer goroutine redraws every second even before the 10s threshold — low

- **Where:** `internal/cli/progress/progress.go:84-102` (`runTimer`).
- **What:** The ticker fires every second and unconditionally takes `ph.spinner.Lock()` + writes `ph.spinner.Suffix`. For the first 10 seconds, `suffix(elapsed)` returns a string identical to what's already set (no timer segment yet). The lock + suffix mutation happens anyway.
- **Why it matters:** Not a perf issue (a 1/s lock is nothing), but the intent — "only tick after 10s" — could be expressed directly. It also papers over a subtler bug: if `showTimerAfter` were ever raised, the ticker would still wake every second.
- **Suggested fix:** In `runTimer`, skip the update when `time.Since(ph.start) < showTimerAfter`, or start the ticker with `time.AfterFunc(showTimerAfter, ...)` and only then begin the 1s loop. Bonus: guard with a `last` string so subsequent `suffix(elapsed)` values that didn't tick a new second don't redraw.

### F10. `runVersion` in lakitu differs from drift's `emitVersion` — low

- **Where:** `internal/cli/lakitu/lakitu.go:123-136` vs. `internal/cli/drift/drift.go:202-227`.
- **What:** Drift's `emitVersion` prints `drift <ver> (<short-commit>)` with commit suffix logic; lakitu prints `lakitu %s` (version only). Both take a `versionCmd{Output string}` with the same `enum:"text,json"` shape but inline the logic twice.
- **Why it matters:** Small user-facing inconsistency (lakitu version strings can't tell you which commit you're on) and literal duplication of the json/text branch.
- **Suggested fix:** Promote drift's `emitVersion` + `formatVersionText` to `internal/version/emit.go` as `version.Emit(w io.Writer, binaryName, format string) error`. Both CLIs call it with their name. Deletes ~20 LOC and unifies behaviour.

### F11. clihelp ignores the NAME-line palette — low

- **Where:** `internal/clihelp/clihelp.go:28-74` (`Render`).
- **What:** Renders a plain-text doc with no styling at all; drift's custom `writeDriftHelp` (chunk 1) does its own palette-aware rendering and bypasses `clihelp` entirely for the curated view. `clihelp` is used by `lakitu help` and `drift help --full` only — both on TTY, both would benefit from bold section headers.
- **Why it matters:** Subtle visual inconsistency: on a TTY, `drift help` is styled, `drift help --full` and `lakitu help` are not. The `clihelp.Doc` struct doesn't even accept a palette, so callers can't thread one through.
- **Suggested fix:** Add `Palette *style.Palette` to `Doc` (or pass `w io.Writer` through `style.For`), bold the NAME/COMMANDS/SECTION titles. Low priority since `help` output is LLM-targeted and plain text is fine.

## Nothing to flag

- `cmd/lakitu/main.go` — tight 45-line entrypoint with the right panic handler for the `rpc` stdout invariant.
- `internal/cliscript/cliscript.go` — 30 LOC, does exactly one job (wire drift/lakitu into testscript).
- `internal/cli/lakitu/subcommands.go` command structs — the struct-per-subcommand pattern reads cleanly even if the runXxx wrappers are thin.
- `internal/clihelp/sections.go` — small, derives from `wire.Methods()` / `config.GarageSubdirs` so it self-updates.
- `style.Palette` nil-safety — `p == nil || !p.Enabled` short-circuit on every method is the right call; removes every `if p != nil` check at call sites.
