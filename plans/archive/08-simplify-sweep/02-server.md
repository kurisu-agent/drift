# Simplify sweep — Server runtime

**Paths reviewed:** `internal/server/` (server.go, character.go, chest.go, config.go, info.go, kart.go, kart_new.go, kart_lifecycle.go, kart_migrate.go, kart_autostart.go, tune.go, run.go, verify.go, version.go)
**Agent:** Opus 4.7 (1M context), read-only review

## Summary

- **F1 (high)** `VerifyHandler` hard-codes a default `devpod.Client{}` with no pinned binary — the circuit's `server.verify` probes a different devpod than the one kart lifecycle handlers actually use. Real bug: `lakitu verify` can report a mismatch against a `$PATH` devpod that is never invoked for real work.
- **F2 (high)** `envKVPairs` is byte-for-byte duplicated across `internal/server/kart.go:438` and `internal/kart/new.go:330`; chest-ref validation/dechest logic is triplicated across `resolveEnvBlock`, `resolveTuneEnv`, and `resolvePATSecret`.
- **F3 (med)** `kart_migrate.go:109` re-parses the server config path instead of the `Deps.serverConfigPath()` helper, because `KartMigrateDeps` has no access to `*Deps`. Same leaky split is already patched awkwardly in lakitu.go (two identical `devpod.Client{}` constructions, two `server.Deps{GarageDir: garage}`).
- **F4 (med)** Autostart marker file path is built in four places (`kart.go:345`, `kart_autostart.go:76`, `kart_lifecycle.go:365` removeKartDir dir join, `internal/kart/new.go:297`) — one typo and the marker writes and reads disagree silently.
- **F5 (med)** `kartListHandler` runs N `devpod status` subprocesses sequentially. For a circuit with 10 karts that's 10 serial shell-outs plus the one `devpod list` already paid for.

## Findings

### F1. `server.verify` uses an unpinned devpod client — high

- **Where:** `internal/server/verify.go:32-53`; wiring at `internal/cli/lakitu/lakitu.go:182-193`.
- **What:** `VerifyHandler` is a package-level function registered without dependencies. `verifyNow(ctx, &devpod.Client{})` constructs a *fresh, default* `devpod.Client` with no `Binary`, no `DevpodHome`, no `Mirror`. Meanwhile lakitu wires the kart handlers with `&devpod.Client{Binary: pinnedBin, DevpodHome: driftDevpod}`.
- **Why it matters:** `server.verify` is the whole point of the "check the circuit is healthy" round-trip. It reports `Actual` from `$PATH/devpod` while `kart.new` / `kart.start` invoke `pinnedBin`. On a circuit where `$PATH` has an older/newer devpod than the pinned one, `verify` lies: it either passes (green) while karts fail, or reports mismatch (red) on a binary nothing actually runs. Also ignores `DEVPOD_HOME`, so its probe reads a different garage than kart lifecycle does.
- **Suggested fix:** Promote `VerifyHandler` to a method on a `VerifyDeps` (or reuse `KartDeps`/`*Deps`) carrying the same `*devpod.Client` the lifecycle handlers use. Register via `reg.Register(wire.MethodServerVerify, d.VerifyHandler)` from `server.go`. Delete the default-client shortcut in `verifyNow`.

### F2. Triplicated chest-ref resolution + duplicated `envKVPairs` — high

- **Where:** `internal/server/kart.go:404-433` (`resolveEnvBlock`), `internal/server/kart_new.go:195-249` (`resolveTuneEnv`), `internal/server/kart_new.go:132-170` (`resolvePATSecret` + `resolveChestRef`). Also `envKVPairs` duplicated at `internal/server/kart.go:438-452` and `internal/kart/new.go:330-344`.
- **What:** All three dechest helpers do the same work: trim whitespace, require `chest:` prefix, backend.Get, wrap `chest_entry_not_found` with structured data. The block-walker in `resolveTuneEnv` is nearly a superset of `resolveEnvBlock`, but they return different types (`kart.ResolvedTuneEnv` vs `map[string]string`) and produce subtly different error messages for the same failure ("kart.new: env.X.Y..." vs "env.X.Y..."). `envKVPairs` is byte-identical across two packages.
- **Why it matters:** Every new injection site (e.g. a future `env.post` block, or a per-character env) ends up pasted in a fourth place. The `chest:` literal is also scattered — `chestRefPrefix` exists in character.go but kart.go/kart_new.go hard-code the literal string, so a rename would miss them.
- **Suggested fix:** Add `dechestRef(backend chest.Backend, field, key, ref string) (string, error)` in a shared helper (either a new `internal/server/chestref.go` or lifted to `internal/chest`) that encapsulates the prefix check, Trim, Get, and `chest_entry_not_found` enrichment. Have `resolveEnvBlock` iterate over it; have `resolveTuneEnv` call `resolveEnvBlock` three times (one per block) and assemble the `ResolvedTuneEnv`. Move `envKVPairs` to `internal/kart` (already imported by server) and delete the server-side copy. Export `chestRefPrefix` from `internal/chest` and use it everywhere (or export from the new helper).
- **Cross-ref:** chunk 3 (`internal/kart/`) owns the other `envKVPairs` copy.

### F3. `KartMigrateDeps` leaks config-path logic because it can't reach `*Deps` — med

- **Where:** `internal/server/kart_migrate.go:107-113`; also `internal/cli/lakitu/lakitu.go:180-194` (duplicate `&devpod.Client{...}` + two `&server.Deps{GarageDir: garage}`).
- **What:** `KartMigrateDeps` embeds `KartDeps` but has no reference to `*Deps`, so the handler reimplements `filepath.Join(d.GarageDir, "config.yaml")` inline instead of `d.serverConfigPath()`. In lakitu.go the same split forces the caller to create two `server.Deps{}` values and construct two identical `devpod.Client{}` values.
- **Why it matters:** The server config path has a fallback when `GarageDir` is empty (`config.ServerConfigPath()`); the inline join silently ignores that fallback. Also invites drift: change the server config filename once and this call site breaks silently because it doesn't go through the shared helper.
- **Suggested fix:** Either (a) add `*Deps` to `KartMigrateDeps` (like `KartNewDeps.Deps`), or (b) promote `serverConfigPath()` and `loadServerConfig()` to exported `Deps.ServerConfigPath()` / `Deps.LoadServerConfig()` and give `KartDeps` a `Server *Deps` field. Option (b) collapses the lakitu.go duplication too.

### F4. Autostart marker path duplicated in four places — med

- **Where:** `internal/server/kart.go:345` (`kartAutostartEnabled`), `internal/server/kart_autostart.go:76` (`autostartMarkerPath`), `internal/server/kart_lifecycle.go:365` (`removeKartDir` implicitly covers it via RemoveAll), `internal/kart/new.go:297-298` (`writeAutostartMarker`).
- **What:** Four independent `filepath.Join(..., "karts", name, "autostart")` constructions. Three of them are pure path builders; one (`kart_autostart.go`) wraps it in a method.
- **Why it matters:** Changing the marker layout (e.g. moving to a `.autostart` dotfile or a YAML status file) requires finding every call site. The `kartAutostartEnabled` check is trivially a one-liner that could reuse the autostart helper.
- **Suggested fix:** Introduce `func autostartMarkerPath(garageDir, kart string) string` (package-level) in `kart_autostart.go`, use it from `kart.go:345` and `kart/new.go:297`. Or lift all kart-path builders (`kartDir`, `configPath`, `autostartPath`) to an `internal/kart/paths.go` so any future layout change is one file.

### F5. `kartListHandler` is N+1 on `devpod status` — med

- **Where:** `internal/server/kart.go:149-155` calling into `buildInfo` at `kart.go:231` which calls `d.statusFor` per kart.
- **What:** After a single `devpod list` (which already returns every workspace), the loop invokes `devpod status <name>` as a separate subprocess for every kart present in devpod. `devpod list` JSON does not carry status, so a per-kart probe is needed, but they are serial.
- **Why it matters:** A circuit with 10 karts pays ~10× a subprocess exec on every `drift list` / `kart.list`. On Termux where `termuxLinkerWrap` adds overhead, this compounds. User-visible impact: noticeable delay on `drift list` once a developer has more than a handful of karts.
- **Suggested fix:** Run the status probes in parallel with `golang.org/x/sync/errgroup` (already in go.sum via other deps, confirm before claiming), bounded by e.g. `errgroup.SetLimit(4)`. Or fold status into a single upstream `devpod status --all --output json` call if the fork supports it (check `internal/devpod` — outside this chunk). Minimum viable fix: a sync.WaitGroup with a mutex-guarded status map.

### F6. `validateTuneName` reimplements `name.Valid` with a one-character rule change — med

- **Where:** `internal/server/tune.go:46-62`.
- **What:** `tuneNameRE` is `^[a-z][a-z0-9-]{0,62}$` — byte-identical to `name.Pattern`. `validateTuneName` repeats the regex-match + Reserved check but inlines a different reserved set (`none` only, not `default`).
- **Why it matters:** The comment explicitly notes the only semantic divergence is allowing `default`. Reimplementing the regex invites drift; the `With("pattern", tuneNameRE.String())` call exposes the duplicated regex in error data.
- **Suggested fix:** Expose a variant in `internal/name` that takes an explicit reserved set, e.g. `name.ValidateAllowing(kind, s string, allow ...string) error`. Or drop the regex copy and chain: if `s == "none"` reject; else `return name.Validate("tune", s)` — the current `name.reserved` already contains both `default` and `none`, so this needs the helper variant. Alternative: export `name.Pattern` (already public) + `name.Matches(s)` and only special-case `none` locally.

### F7. Parallel List/Show/Remove handlers in tune.go + character.go — med

- **Where:** `internal/server/tune.go:64-91` + `internal/server/character.go:100-127` (List); tune.go:93-106 + character.go:131-144 (Show); tune.go:139-168 + character.go:148-177 (Remove). All share `kartsReferencing` at `character.go:207-242`.
- **What:** The List handler structure is the same for tunes and characters: `ReadDir`, filter non-yaml, `load`, append, sort by name. Show is `if name == "" { err }; load; return result`. Remove is `stat; kartsReferencing; os.Remove; return`. Each is ~25-30 lines duplicated.
- **Why it matters:** Third-entity handlers (if any are added — e.g. `machines`) will paste a fourth copy. Error messages drift slightly ("tune.list: %v" vs "character.list: %v") — fine today, surprising later.
- **Suggested fix:** A generic `listYAMLEntries[T any](dir string, load func(string) (*T, error)) ([]result[T], error)` + `showYAMLEntry[T]` + `removeYAMLEntry`. Probably not worth a full generics pass — lower-effort win is `listYAMLNames(dir) ([]string, error)` covering just the readdir + filter + TrimSuffix + sort part used by both lists, and also usable by `listGarageKarts`.

### F8. `garageByName` is a pointless re-copy — low

- **Where:** `internal/server/kart.go:129-132`.
- **What:** `listGarageKarts` returns `map[string]KartConfig`. The handler then copies it into `garageByName := make(map[string]KartConfig, len(garage))` and loops with `garageByName[name] = cfg`. No transformation, no type change.
- **Suggested fix:** Delete lines 129-132 and index `garage` directly at 152.

### F9. Ten `var p struct{}; rpc.BindParams(params, &p)` call sites — low

- **Where:** character.go:101, chest.go:72, config.go:43, info.go:36, kart.go:111, kart_migrate.go:49, run.go:23, tune.go:65, verify.go:33, version.go:20.
- **What:** Every no-params handler does the same 3-line boilerplate. `rpc.BindParams` already tolerates empty input (returns nil on whitespace-only raw).
- **Suggested fix:** Either (a) accept that `BindParams(params, &struct{}{})` is idempotent and drop the binding for no-params handlers (lose unknown-field rejection for empty bodies — low-value), or (b) add `rpc.NoParams(raw json.RawMessage) error` that wraps the pattern; three-line handlers become one-line.
- **Cross-ref:** chunk 5 (`internal/rpc`) owns the helper if approach (b) wins.

### F10. `requireKartName` vs `name.Validate("kart", ...)` inconsistency — low

- **Where:** `internal/server/kart_autostart.go:109-114`; versus callers using `name.Validate("kart", ...)` in `internal/kart/new.go:55` and `internal/cli/drift/ssh_proxy.go:63`.
- **What:** `kart.enable` / `kart.disable` only check name is non-empty; no regex validation. Every other kart-name path validates shape.
- **Why it matters:** A malformed kart name can write an autostart marker file under an unusual path (`karts/../../foo/autostart` would still be caught by `filepath.Join` but e.g. `KART..with dots` sails through). Low probability but inconsistent with the rest of the surface.
- **Suggested fix:** Replace `requireKartName(p.Name)` with `name.Validate("kart", p.Name)` in both autostart handlers.

### F11. `bindLifecycleParams` and `requireKartName` are near-twins; leaky error-wrapping in `wrapSystemdError` — low

- **Where:** `kart_lifecycle.go:292-301` vs `kart_autostart.go:109-114`; `kart_autostart.go:97-107` (`wrapSystemdError`).
- **What:** Both helpers reject empty names with slightly different messages. `wrapSystemdError` returns `rpcerr.CodeDevpod` for a systemctl failure — confusing (not a devpod error). The code field is treated as category-ish; using `CodeDevpod` for a systemd surface is a small abstraction leak.
- **Suggested fix:** Collapse both into a shared `requireKartName` that calls `name.Validate`; rename/reuse the systemd-error code (add `rpcerr.CodeSystemd` or drop back to `CodeInternal`).

### Low-severity batch (capped)

Also noticed across the package — picking the top 3, skipping 7 nits:

- **L-a** `server.go:58-70` `driftHome`/`garageDir` are a near-copy-paste pair; a single `resolvePath(override string, fallback func() (string, error)) (string, error)` would collapse them.
- **L-b** `kart.go:207-209` sets both `Tune` and `Character` from `cfg.*` but the field docs on `KartInfo` note `Character` is required ("`json:\"character\"`") while `Tune` is `omitempty`. `cfg.Character` can legitimately be empty for a kart created with `--character ""`; the non-omitempty JSON key becomes `""` silently — intentional? Worth a doc-comment if so.
- **L-c** `kart_migrate.go:123` uses `\x00` as a delimiter in a plain-string map key — safe today but surprising. A `type migrateKey struct{ ctx, name string }` as the map key is stdlib-idiomatic.

## Nothing to flag

- **run.go, version.go, info.go** — clean thin adapters; nothing to simplify.
- **chest.go** — handlers are appropriately small given the backend abstraction; only the chest-prefix string shows up (see F2).
- **kart_lifecycle.go log handling (`classifyLogLines` + `filterLogLines`)** — single-purpose, well-commented, correct filter order; no obvious simplification.
- **`kartErrCleanup` in `internal/kart/new.go`** — outside this chunk but referenced; not reviewed here.
