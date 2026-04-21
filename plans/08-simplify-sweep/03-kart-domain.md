# Simplify sweep — Kart domain + config + data types

**Paths reviewed:** `internal/kart/`, `internal/config/`, `internal/chest/`, `internal/name/`, `internal/model/`, `internal/run/`
**Agent:** claude-opus-4-7 (1M context), read-only sweep

## Summary

- Kart on-disk paths (`<garage>/karts/<name>[/config.yaml|/autostart|/status]`) are rebuilt from raw `filepath.Join` literals in 7+ spots across `internal/kart` and `internal/server`; single `config.KartDir(...)` / `config.KartConfigPath(...)` helpers would remove an entire class of drift risk.
- `KartConfig` is defined twice with overlapping fields — an anonymous `onDisk` struct in `kart/new.go:263-271` that writes the file and `server.KartConfig` in `server/kart.go:40-52` that reads it. Divergence is waiting to happen (e.g. `user`/`shell`/`image`/`workdir` are read-only today because the writer doesn't know about them).
- `chest:` is a de-facto prefix enum but lives as bare `"chest:"` string literals in ≥6 call sites; only `server/character.go:50` defines a `chestRefPrefix` constant and even that file doesn't reuse it for the trim operation. Promote to `chest.RefPrefix` + a `chest.ParseRef(s) (name string, ok bool)` helper.
- `CircuitNameRE` in `config/server.go:23` and `name.Pattern` in `name/name.go:15` are the same regex (one has a cosmetically different `_` vs `-` in run names — but circuits strictly do not — and they target the same DNS-slug shape). Either reuse `name.Valid` for circuit names, or have `config` re-export a shared regex. Run names are a different regex (`[a-z][a-z0-9_-]…`) so they stay separate but should share a constant too.
- `kart.Character` (`kart/flags.go:40-47`) and `server.Character` (`server/character.go:22-28`) duplicate 4 of 5 fields with different naming (`PAT` vs `PATSecret`); promote one to `model.Character` and let the server resolve PAT→literal at the boundary where it already does.

## Findings

### F1. Duplicate kart on-disk path composition — med

- **Where:** `internal/kart/new.go:59,145,166,181,213,259,294,297-298,303-308` + `internal/server/kart.go:292,323,327,345` + `internal/server/kart_lifecycle.go:365` + `internal/server/kart_autostart.go:76` + tests throughout.
- **What:** Every accessor reinvents `filepath.Join(garageDir, "karts", name[, "config.yaml"|"autostart"|"status"])`. 13+ production and 5+ test sites.
- **Why it matters:** A directory-layout change (e.g. moving autostart into `config.yaml` as a field — which is already plausible given the bool lives nowhere else) needs coordinated edits across packages. Also causes subtle duplication in `server/kart.go:326-327` where the "config missing but dir exists" branch has to rebuild the dir path it just stat'd.
- **Suggested fix:** Add to `internal/config/paths.go`:

  ```go
  func KartsDir(garageDir string) string       { return filepath.Join(garageDir, "karts") }
  func KartDir(garageDir, name string) string  { return filepath.Join(KartsDir(garageDir), name) }
  func KartConfigPath(g, n string) string      { return filepath.Join(KartDir(g, n), "config.yaml") }
  func KartAutostartPath(g, n string) string   { return filepath.Join(KartDir(g, n), "autostart") }
  func KartStatusPath(g, n string) string      { return filepath.Join(KartDir(g, n), "status") }
  ```

  Replace call sites mechanically; no import cycles (kart, server already depend on config).
- **Cross-ref:** Chunk 2 (server) touches the same names.

### F2. `KartConfig` struct exists in two packages with divergent fields — high

- **Where:** `internal/kart/new.go:263-271` (anonymous `onDisk` struct used for writing) vs `internal/server/kart.go:40-52` (`KartConfig` used for reading).
- **What:** The writer only knows 7 fields; the reader knows 11 (extra: `User`, `Shell`, `Image`, `Workdir`). The extra fields are never persisted by `drift new` — they're only populated by some other path (or never), silently drifting from the writer's schema.
- **Why it matters:** A field added to the writer without the reader (or vice-versa) yields silent data loss. Tests that round-trip via `writeKart` (server test helper) don't catch it because they bypass `writeKartConfig`.
- **Suggested fix:** Promote a single `model.KartConfig` with all fields and have both packages use it. If `User`/`Shell`/`Image`/`Workdir` are dead fields (grep shows only read sites in `kart.go:sourceFromConfig` + display code), delete them. Relatedly, `model.TuneEnv` and `model.MigratedFrom` already live in `model/types.go` for exactly this reason — extend the same approach.

### F3. `chest:` prefix is stringly-typed across the codebase — med

- **Where:** `kart/flags.go:199,206`; `server/kart_new.go:137,141,159,218,223`; `server/kart.go:414,419`; `server/character.go:50` (declares constant but doesn't use it for trim).
- **What:** Every site does `strings.HasPrefix(x, "chest:")` + `strings.TrimPrefix(x, "chest:")`. The one existing constant `chestRefPrefix` is package-private to `server/`.
- **Why it matters:** Adding a second ref scheme (`env:`, `file:`, `vault:`) requires grepping for `"chest:"` everywhere. Also every trim site re-does the prefix check, inviting `TrimPrefix` on a non-prefixed value (which is a no-op that silently treats "naked" as a ref — actually currently fine but fragile).
- **Suggested fix:** In `internal/chest/chest.go` add:

  ```go
  const RefPrefix = "chest:"
  func ParseRef(s string) (name string, ok bool) {
      if !strings.HasPrefix(s, RefPrefix) { return "", false }
      return strings.TrimPrefix(s, RefPrefix), true
  }
  ```

  Replace all `"chest:"` literals; delete `server.chestRefPrefix`.
- **Cross-ref:** Chunk 2 touches the server side; chunk 4 (warmup) has one more use at `internal/warmup/warmup.go:356`.

### F4. Circuit-name regex duplicates the name-package pattern — med

- **Where:** `internal/config/server.go:23` declares `CircuitNameRE = ^[a-z][a-z0-9-]{0,62}$`; `internal/name/name.go:15` declares `Pattern = ^[a-z][a-z0-9-]{0,62}$`. Identical strings.
- **What:** Two source-of-truth regexps for the same slug shape. `config.CircuitNameRE` is used in `config/server.go:50,83`, `config/client.go:27`, `warmup/warmup.go:195`, `cli/drift/circuit.go:85`; `name.Valid`/`name.Validate` is used for kart/character names.
- **Why it matters:** Reserved-word enforcement diverges — circuits currently **can** be named `default` or `none` (the config regex allows it) but karts can't (the `name` package forbids it via `reserved`). This is almost certainly a bug for `default` given that `default_circuit` exists as its own field.
- **Suggested fix:** Have `config` call `name.Valid` / `name.Validate("circuit", ...)` — or at minimum, declare `CircuitNameRE = regexp.MustCompile(name.Pattern)` so the literal lives once. Decide explicitly whether "default"/"none" are reserved circuit names.

### F5. `Character` struct is duplicated across kart and server — med

- **Where:** `internal/kart/flags.go:40-47` (`Character{GitName, GitEmail, GithubUser, SSHKeyPath, PAT}`) vs `internal/server/character.go:22-28` (`Character{GitName, GitEmail, GithubUser, SSHKeyPath, PATSecret}`).
- **What:** Same 4-field shape plus a token field that differs only in whether it's the dechested literal (`PAT`) or the chest ref (`PATSecret`).
- **Why it matters:** Every field add requires two edits; the divergent token naming makes the boundary between "on disk" and "resolved" implicit rather than explicit.
- **Suggested fix:** One `model.Character{GitName, GitEmail, GithubUser, SSHKeyPath, PATSecret}` on disk, with a `model.ResolvedCharacter` (or a `Resolved bool` invariant) that carries the dechested PAT. Put the resolver (`server/kart_new.go:resolvePATSecret`) at the boundary and have `kart.Resolver.LoadCharacter` return the resolved form — matches the comment at `kart/flags.go:98-99`.

### F6. `indexByte` / `toLowerASCII` reimplement stdlib for a single call — low

- **Where:** `internal/config/server.go:89-111`.
- **What:** Hand-rolled `indexByte` and `toLowerASCII` "to keep this file std-lib-only" — but `strings.IndexByte` and `strings.ToLower` are std-lib; the comment's reasoning doesn't hold.
- **Why it matters:** 20 extra lines of code, one more thing to test. The `strings` package is imported by half the packages anyway.
- **Suggested fix:** Replace with `strings.IndexByte(h, '.')` and `strings.ToLower(h)`. The ASCII-only constraint doesn't matter because the result is fed back into `CircuitNameRE` which rejects non-ASCII anyway.

### F7. `kart.writeKartConfig` bypasses `config.marshalAndWrite` — low

- **Where:** `internal/kart/new.go:260-295` vs `internal/config/io.go:34-40` (`marshalAndWrite`).
- **What:** `kart.writeKartConfig` does `yaml.Marshal` + `config.WriteFileAtomic` inline. `config.marshalAndWrite` does the same two operations but is package-private.
- **Why it matters:** Minor duplication; more importantly, the unexported helper is a signal the author didn't anticipate an external caller, and every other caller (LoadServer/SaveServer/SaveClient) goes through it.
- **Suggested fix:** Export as `config.MarshalAndWrite` (or `config.WriteYAML`) and call it from `kart/new.go`, `server/tune.go:127`, `server/character.go:88`. Kills a `yaml.Marshal` import in each call site too.

### F8. `WriteFileAtomic` Close/cleanup duplication — low

- **Where:** `internal/config/io.go:57-75`.
- **What:** Four near-identical `_ = tmp.Close(); cleanup(); return fmt.Errorf(...)` blocks for write / chmod / sync / close failures. The close-on-error path is correct but repeated.
- **Why it matters:** Easy to get wrong next time; the pattern tempts copy-paste drift (e.g. forgetting `cleanup()` on one branch).
- **Suggested fix:** Collapse into one labeled cleanup with named returns:

  ```go
  defer func() {
      if err != nil { _ = tmp.Close(); _ = os.Remove(tmpPath) }
  }()
  ```

  Then each operation is a single `if err := ...; err != nil { return fmt.Errorf(...) }`.

### F9. `repeat` reimplements `strings.Repeat` in test file — low

- **Where:** `internal/name/name_test.go:78-84`.
- **What:** A `repeat(s, n)` helper that does exactly what `strings.Repeat` does.
- **Why it matters:** Pure nit but it's the only non-trivial code in the test file that isn't test logic.
- **Suggested fix:** Replace calls with `strings.Repeat("x", 62)`; delete the helper.

### F10. `ensureEmbedded` and `ensureManaged` split is premature — low

- **Where:** `internal/config/claude_md.go:56` (`ensureManaged`) vs `internal/config/runs_yaml.go:49` (`ensureEmbedded`).
- **What:** Two helpers for "write embedded file at path if not present". `ensureEmbedded` is the no-marker variant; `ensureManaged` adds the user-split marker logic. They diverge only on the marker branch.
- **Why it matters:** A third embedded file will almost certainly reach for one of these and get the wrong semantics (if you pick `ensureEmbedded` by default you skip any future header refresh). The `runs.yaml` and `scaffolder.md` files explicitly don't want the marker, so the split is real — but the naming doesn't signal the distinction.
- **Suggested fix:** Rename to `ensureEmbeddedStatic` and `ensureEmbeddedManaged`; add a doc comment on each saying which to pick. Alternatively, fold into one `ensureEmbedded(path, parentDir string, body []byte, managed bool)`.

### F11. `kart.New` has TOCTOU on kart dir existence — low

- **Where:** `internal/kart/new.go:59-85`.
- **What:** `os.Stat(kartDir)` → if absent, later `writeKartConfig` calls `os.MkdirAll` + `config.WriteFileAtomic`. Between stat and write, another `drift new` for the same name can race in.
- **Why it matters:** Low-severity because two concurrent `drift new <same>` is already user error and the devpod side will still error, but the rpcerr returned will be wrong (devpod up failure vs name_collision).
- **Suggested fix:** Use `os.Mkdir` (not MkdirAll) on `kartDir` as the existence probe — `ErrExist` is atomic. The devpod list check still happens for collision-vs-stale distinction but moves after the directory is exclusively owned.

### F12. `kart.Resolved.SourceMode` is a stringly-typed enum — low

- **Where:** `internal/kart/flags.go:75` (comment says `"clone" | "starter" | "none"`), `internal/kart/new.go:106-123` (switch on string), `internal/server/kart.go:365-375`.
- **What:** The comment literally documents a closed enum in a comment because Go doesn't have the type.
- **Why it matters:** Typos land at runtime. Adding a new mode (e.g. `"archive"`, `"image"`) has no compile-time safety.
- **Suggested fix:** `type SourceMode string` in `model/types.go` with `SourceModeClone`/`SourceModeStarter`/`SourceModeNone` constants, following the existing `wire.RunMode` precedent. Single-line change to `Resolved.SourceMode` and `KartConfig.SourceMode` fields (YAML tag keeps `omitempty` and the raw string representation; enum only affects Go callers).

### F13. Kart autostart marker is a sentinel file — low

- **Where:** `internal/kart/new.go:297-299` (`writeAutostartMarker`), `internal/server/kart.go:344-350` (`kartAutostartEnabled`), `internal/server/kart_autostart.go:76` (resolves path).
- **What:** Autostart state = "does a zero-byte file named `autostart` exist next to config.yaml?". Meanwhile `KartConfig` already has structured YAML for every other setting and the `Autostart bool` field on `kart.Flags`/`kart.Resolved` already exists in memory.
- **Why it matters:** Cross-package reach for a single bool; three files to touch for one state transition; `os.Stat`-based existence check at every query.
- **Suggested fix:** Add `Autostart bool `yaml:"autostart,omitempty"`` to `KartConfig`; stop writing the sentinel file. Backwards compat: read the sentinel once on load and migrate it into the YAML on next write. If the sentinel is kept deliberately (e.g. for shell scripts to `[ -f ~/.drift/garage/karts/x/autostart ]`), a comment saying so would help — couldn't find any such consumer.

### F14. `Registry.Get`/`Sorted`/map layout duplicates standard iteration — low

- **Where:** `internal/run/registry.go:83-97`.
- **What:** `Registry` wraps `Entries map[string]Entry` and exposes `Get` + `Sorted`. `Get` is a bare map delegate; `Sorted` rebuilds a slice on every call.
- **Why it matters:** `Get` adds no value over direct map access (the map field is exported). `Sorted` makes a fresh allocation per call — fine at this scale but surprising if it ever becomes a hot path (e.g. every RPC revalidates on `run.list`).
- **Suggested fix:** If `Get` is keeping the field private an option, make the map field unexported — otherwise delete `Get`. Cache `Sorted` behind a `once` or compute at parse time (the registry is immutable after load). Low priority; flag only because the API surface is small enough to polish.

### F15. `splitOnManagedMarker` hand-rolls line iteration — low

- **Where:** `internal/config/claude_md.go:107-128`.
- **What:** Manual loop advancing `pos` across newlines to find a line-prefix match.
- **Why it matters:** 22 lines for what `bufio.Scanner` or `bytes.Split(b, []byte("\n"))` + index tracking expresses in 5.
- **Suggested fix:** Either iterate `bytes.Split(content, []byte{'\n'})` keeping a running offset, or use `bytes.Index(content, []byte("\n<!-- drift:user"))` as a fast path and fall back to the beginning-of-file case.

## Nothing to flag

- `internal/run/template.go` — small, focused, tests cover edge cases (missing args, shell quoting). Clean.
- `internal/run/types.go` — trivial aliases.
- `internal/chest/chest.go` + `internal/chest/yamlfile.go` — tight, one backend, atomic write via `config.WriteFileAtomic`. The "rewrite empty map to zero bytes" trick at `yamlfile.go:108-112` is worth keeping.
- `internal/model/types.go` — the right shape; finding F2/F5 recommend extending it, not changing what's there.
- `internal/kart/starter.go` — clean separation of command execution via `driftexec.Runner`. `authorFor`/`inheritedEnv` are appropriately scoped.
- `internal/kart/devcontainer.go` — well-bounded fetcher with size limit, timeout, fake-fetcher injection for tests.
