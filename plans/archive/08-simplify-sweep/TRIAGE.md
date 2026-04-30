# Simplify sweep — triage

Aggregate of the 7 chunk reports in this directory. ~81 findings total: **4 high, 28 med, 49 low**. Clusters are grouped across chunks; each bullet links to the original finding(s) by `<chunk>:F<n>`.

Columns: **Cluster** → **Severity** → **Effort** → **Bucket**.

## Fix now (high-severity bugs + high-leverage low-risk cleanups)

| # | Cluster | Sev | Effort | Details |
|---|---------|-----|--------|---------|
| 1 | **`VerifyHandler` uses an unpinned devpod** | high | S | `server.verify` probes `$PATH/devpod` while kart lifecycle uses the pinned binary — `lakitu verify` can lie either direction. Fix: promote to a `*Deps` method and reuse the wired `devpod.Client`. — `02:F1` |
| 2 | **`KartConfig` writer/reader diverge** | high | M | Writer (7 fields) and reader (11 fields) live in different packages; fields drop silently. Fix: one `model.KartConfig`, used by both. Also collapses the duplicate `Character` struct. — `03:F2`, `03:F5` |
| 3 | **Triplicated chest-ref + duplicated `envKVPairs`** | high | M | `resolveEnvBlock`, `resolveTuneEnv`, `resolvePATSecret` all redo the chest-prefix dance; `envKVPairs` is byte-identical in two packages; `"chest:"` literal scattered across 6+ sites. Fix: promote `chest.RefPrefix` + `chest.ParseRef`; single dechest helper. — `02:F2`, `03:F3`, `04` (warmup use) |
| 4 | **`runNew`/`runConnect` bypass `deps.call`** | high | S | The two most user-visible commands have un-stubbable primary paths. Fix: route through `deps.call` like every other handler. — `01:F1` |
| 5 | **Dead subsystem: `CompatChecker`** (~120 LOC + test) | med | XS | Zero callers. Delete file + test + the one stringly-typed `version_mismatch` Type. — `05:F1` |
| 6 | **Kart-path builders scattered** | med | S | 13+ sites rebuild `filepath.Join(garage, "karts", name, …)`; marker-file path exists in 4 places. Fix: `config.KartDir`/`KartConfigPath`/`KartAutostartPath`. Pairs naturally with clusters 2 and 3. — `02:F4`, `03:F1` |
| 7 | **Stringly-typed status in `connect`** | med | XS | `internal/connect` compares `info.Status` to `"running"/"stopped"/"busy"` while `devpod.Status` constants exist on the same wire shape. Typed switch drops 6 literals. — `04:F1` |
| 8 | **`TypeCircuitNotFound` missing** | med | XS | `circuit.go` uses `TypeKartNotFound` for a missing circuit; `ssh_proxy.go` hard-codes the literal `"circuit_not_found"`. Fix: add the constant, use it in both sites. — `01:F4` |
| 9 | **`rpcerr.MarshalJSON` vs `Wire()`** | med | XS | Two implementations, already silently divergent on error handling, with a marshal/unmarshal round-trip in `cmd/lakitu/main.go`. Fix: delete `MarshalJSON`, have panic path call `Wire()` directly. — `05:F2` |
| 10 | **Dead exports in transport** | low | XS | `Registry.Has`, `wire.EncodeRequest`, `Client.nextID`, `Result.ExitCode` have no callers. Delete together. — `05:F3`, `F4`, `F5`, `F6` |
| 11 | **stdlib reinventions** | low | XS | `containsString` → `slices.Contains`; `or` → `cmp.Or`; `toLowerASCII`/`indexByte` → `strings.*`; `trimJSONSpace` → `bytes.TrimSpace`; `repeat` → `strings.Repeat`. Mechanical, one PR. — `01:F7`, `F9`, `03:F6`, `03:F9`, `04:F5` |

## Fix later (worthwhile, but bigger surface or lower leverage)

| # | Cluster | Sev | Effort | Details |
|---|---------|-----|--------|---------|
| 12 | **`Registry()` rebuilds per lakitu subcommand** | med | M | Pure-config subcommands trigger the 117MB `EnsurePinned` path. Split into non-devpod / devpod-backed registration; gate devpod on method. — `06:F1` |
| 13 | **SSH 4-step setup duplicated across 3 CLI sites** | med | S | `EnsureInclude` → `EnsureSocketsDir` → `WriteCircuitBlock` → `EnsureWildcardBlock`. Fix: one `Manager.InstallCircuit(...)`. — `04:F2` (owns caller-side finding from chunk 1) |
| 14 | **Parallelization wins** (4 separate sites) | med | M | `kartListHandler` is N+1 on `devpod status` (`02:F5`); `runStatus` probes circuits serially (`01:F13`); `warmup.runSummary` fans sequentially (`04:F4`); integration tests rebuild binaries N times (`07:F1`). All `errgroup.SetLimit(4)` candidates. |
| 15 | **`KartMigrateDeps` can't reach `*Deps`** | med | S | Handler reimplements `serverConfigPath` inline; lakitu.go has two identical `devpod.Client{}` + `server.Deps{}` copies. Fix: export `Deps.ServerConfigPath/LoadServerConfig` and thread `Server *Deps` into `KartMigrateDeps`. — `02:F3` |
| 16 | **Circuit-name regex duplicates `name.Pattern`** | med | S | Two copies of the same regex; circuits currently allow `default`/`none` even though karts don't — likely a latent bug. Fix: have `config.CircuitNameRE` reuse `name.Pattern` + decide reserved set. — `03:F4` |
| 17 | **Init sequence duplicated in runInit + serverInitHandler** | med | S | Four-step sequence with divergent paths/error shapes. Fix: `config.InitGarageFull` returning `InitResult`. — `06:F2` |
| 18 | **JSON emit boilerplate × 7** | med | S | Every `--output json` branch hand-rolls `json.Marshal` + `Fprintln` + errfmt. Fix: one `emitJSON(io, v)`. — `01:F5` |
| 19 | **Accent-column table styler × 5** | med | S | 5 callers rebuild the same closure with inline `lipgloss.Color("6")` duplicating `Palette.accent`. Fix: helper in `table.go`; drops `lipgloss` imports from list/run/status/circuit. — `01:F2` |
| 20 | **Two prompt UX flavors** | med | S | `bufio` prompts in `circuit.go`/`new.go` vs `huh` everywhere else; bufio path has dead-code remnant (`_ = defaultRow`). Fix: port to `huh`. — `01:F6` |
| 21 | **Parallel List/Show/Remove handlers (tune + character)** | med | M | ~25 LOC duplicated per verb. Fix: either a minimal `listYAMLNames(dir)` helper or a generics pass. — `02:F7`, `03:F10` (`ensureEmbedded` split related) |
| 22 | **Duplicated `resolveCircuit` + reload pattern** | med | S | Two YAML parses per circuit verb. Fix: return `(*config.Client, string, error)`. — `01:F3` |
| 23 | **`validateTuneName` reimplements `name.Validate`** | med | XS | One-character rule difference. Fix: `name.ValidateAllowing(kind, s, allow...)`. — `02:F6` |
| 24 | **Integration test setup duplication** | med | M | `StartCircuit + lakitu init + RegisterCircuit` stanza in 13 tests; `stageLocalStarter` duplicates `Circuit.StageStarter`; `harness.go` 924 LOC mixes 5 concerns; `GOARCH` override forces qemu on Apple Silicon. Cluster as one follow-up. — `07:F1`, `F2`, `F3`, `F4`, `F5` |
| 25 | **Autostart is a sentinel file** | low | S | `Autostart bool` already lives in memory; `os.Stat` on every query. Fix: add YAML field + one-shot sentinel migration. — `03:F13` |
| 26 | **`SourceMode` + other stringly-typed enums** | low | S | Documented closed enum in a comment; add `type SourceMode string` in `model` following `wire.RunMode`. — `03:F12` |
| 27 | **Kong `kctx.Run()` instead of 26-case switch** | low | M | Hand-rolled dispatch on `kctx.Command()` in lakitu (and the same pattern in drift). Touches many commands — do together or skip. — `06:F4` |
| 28 | **`exec.Run` / `Interactive` Cancel-block duplication** | med | S | ~20 lines duplicated on the spawn hot path. Extract `applyCancelAndWaitDelay` + `finishRun`. — `05:F7` |

## Won't fix (or: keep as a note, not a task)

- **`harness.go` split** (`07:F4`). Defer — touches every integration test import. Flag for the next harness-touching PR; don't refactor speculatively.
- **`splitOnManagedMarker` hand-rolled iteration** (`03:F15`). Works, tested, not on a hot path.
- **`kart.New` TOCTOU** (`03:F11`). Two concurrent `drift new <same>` is user error and devpod catches it downstream.
- **`EnsureSocketsDir` double-chmod** (`04:F9`). One syscall, not hot-path.
- **`parseManaged` 1MiB scanner buffer** (`04:F11`). Cosmetic; default buffer is already fine but the oversized call isn't wrong.
- **`Registry.Get`/`Sorted` surface** (`03:F14`). Low leverage — the shape is fine.
- **`InstallDotfiles` wrapper** (`04:F6`), **`osEnviron` dead seam** (`04:F7`). Pick up if touching `devpod.go` anyway; not worth a standalone PR.
- **Progress spinner 1s tick before `showTimerAfter`** (`06:F9`). Lock-per-second is negligible.
- **`clihelp` palette styling** (`06:F11`). Help output is LLM-targeted; plain text is intentional.
- **`slogfmt.Emit` allocates per record** (`05:F9.6`). Drift's log volume is modest; `sync.Pool` would be premature.
- **`semver` parsing order nit in `compat.go`** (`05:F9.5`). Agent self-corrected on review — not a bug. Moot if cluster #5 lands.

## Suggested PR shape

If you want this landed without one giant PR, split along these seams:

1. **Dead-code removal** — clusters 5, 10 (and the `InstallDotfiles` / `osEnviron` notes if convenient). Small, low-risk, easy to review.
2. **stdlib reinventions** — cluster 11. Mechanical, single PR.
3. **Transport fixes** — clusters 1, 9 (`VerifyHandler` + `rpcerr.MarshalJSON`). Thematic.
4. **Domain-model consolidation** — clusters 2, 3, 6 (`KartConfig`/`Character`/chest-ref/path helpers). One PR, touches many files but one cohesive refactor.
5. **Testability** — cluster 4 (`runNew`/`runConnect` → `deps.call`). Small, enables future tests.
6. **Parallelization** — cluster 14. One PR with four `errgroup` uses.
7. **Shared helpers** — clusters 18, 19, 20, 22. CLI surface polish.

Everything else in "fix later" can slide behind ad-hoc PRs as the relevant files get touched.

## Re-run cadence

Findings files in this directory reflect the tree at `main` as of the sweep date. To re-run, delete the findings files (keep the plan + this triage as history) and rerun the 7 parallel agents from the runbook in `plans/08-simplify-sweep.md`.
