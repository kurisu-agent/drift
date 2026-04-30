# Simplify sweep — Transport + utilities

**Paths reviewed:** `internal/rpc/`, `internal/rpc/client/`, `internal/rpcerr/`, `internal/wire/`, `internal/exec/`, `internal/slogfmt/`, `internal/version/`
**Agent:** Opus 4.7 (1M) via general-purpose review against `/simplify` rubric.

## Summary

- Several exported surfaces are unused: `Registry.Has`, `wire.EncodeRequest`, `CompatChecker` / `NewCompatChecker`, `Result.ExitCode`, `Client.nextID`. Cheap wins.
- `rpcerr.Error.MarshalJSON` duplicates `Error.Wire()` logic and is only consumed by a marshal-then-unmarshal round-trip in `cmd/lakitu/main.go` that could call `Wire()` directly. Remove `MarshalJSON` or have the panic path use `Wire()`.
- `exec.Run` and `exec.Interactive` duplicate the full Cancel / WaitDelay / context-wins / ExitError-extraction block. Extract a small helper.
- `rpc/client/compat.go` is a self-contained dead subsystem (~120 LOC + tests) with no non-test callers anywhere in `cmd/` or `internal/cli/`. Delete or wire it up.
- Low-severity nits batched under F9 (parameter sprawl in `slogfmt.styleLevel`, double `os.Getenv` in `runSSHRPC`, silent-swallow of `json.Marshal` error in `Wire`, etc.).

## Findings

### F1. `CompatChecker` is dead code — med

- **Where:** `internal/rpc/client/compat.go:1-122`, `internal/rpc/client/compat_test.go:1-55`.
- **What:** `NewCompatChecker` / `CompatChecker.Check` have zero callers outside the compat_test and the `plans/DONE.md` mention. `grep -R 'Compat' internal/cli cmd/` returns nothing.
- **Why it matters:** Maintains a `sync.Map`-based singleflight, a custom semver parser, and a rpcerr `version_mismatch` Type that is not in the `rpcerr.Type` enum — effectively stringly-typed. Drift versioning is currently un-enforced at the call sites that were supposed to use it.
- **Suggested fix:** Either delete the file + test and drop the "version_mismatch" stringly-typed usage, or wire `CompatChecker.Check` into `internal/cli/drift/deps.go` before every remote `Call`. If kept, promote `"version_mismatch"` to `rpcerr.TypeVersionMismatch` alongside the other typed constants.

### F2. `rpcerr.Error.MarshalJSON` is functionally redundant with `Wire()` — med

- **Where:** `internal/rpcerr/rpcerr.go:95-115` vs `119-135`; only external caller is `cmd/lakitu/main.go:26-31`.
- **What:** Both methods build the same `data` map with injected `type`, then marshal. The lakitu panic handler does `MarshalJSON()` → `json.Unmarshal(&we)` → `EncodeResponse` — a marshal/unmarshal round-trip for data that `Wire()` produces directly as a `*wire.Error`.
- **Why it matters:** Two places to keep in sync, extra allocation on the panic path, and the two implementations diverge in one detail (`MarshalJSON` silently drops the error from the outer `json.Marshal`; `Wire` silently drops it from the inner `data` marshal).
- **Suggested fix:** Delete `MarshalJSON`. Change `cmd/lakitu/main.go:22-38` to:
  ```go
  _ = wire.EncodeResponse(os.Stdout, &wire.Response{JSONRPC: wire.Version, ID: nil, Error: e.Wire()})
  ```
  The ID is already nil/empty in the panic path; `wire.EncodeResponse` fills `JSONRPC` if missing.

### F3. `Registry.Has` is a dead export — low

- **Where:** `internal/rpc/rpc.go:39-42`.
- **What:** `ripgrep '\.Has\(' ...` returns no callers in the whole repo.
- **Suggested fix:** Delete the method. If a future caller needs it, `_, ok := reg.methods[name]` is one line inside the package.

### F4. `wire.EncodeRequest` is unused — low

- **Where:** `internal/wire/wire.go:94-107`.
- **What:** `client.buildRequest` (`internal/rpc/client/client.go:101-117`) uses `json.Marshal` directly and does not call `wire.EncodeRequest`. Integration tests only use `wire.DecodeResponse`. No production caller.
- **Why it matters:** Dead parallel to `EncodeResponse` invites drift (the server path uses `EncodeResponse`, the client rolls its own `json.Marshal`).
- **Suggested fix:** Either delete `EncodeRequest`, or change `buildRequest` to call it for symmetry. Preferred: delete — the client needs `[]byte` not a `Writer`, so `json.Marshal` is the right primitive there.

### F5. `Client.nextID` field is unreachable — low

- **Where:** `internal/rpc/client/client.go:47-48,92-99`.
- **What:** The field has no setter and is `unexported` — no test or production code can populate it. The branch `if c.nextID != nil` is permanently false.
- **Suggested fix:** Delete the field and its branch in `allocID`; return `json.RawMessage("1")` directly (inline since `allocID` then has one caller). The comment claims it's "for tests" but nothing plumbs it.

### F6. `exec.Result.ExitCode` is always zero — low

- **Where:** `internal/exec/exec.go:41-46`; only non-test reader is `exec_test.go:37-38` (which asserts it's 0).
- **What:** Documented "always 0 — non-zero exits return *Error instead", so production code gets no signal from it. It's an attractive nuisance: a reader sees `Result.ExitCode` and may write `if res.ExitCode != 0` branching that never fires.
- **Suggested fix:** Delete the field. Update the test. Keep `Error.ExitCode` (which is the real carrier).

### F7. `exec.Run` and `exec.Interactive` duplicate the Cancel / WaitDelay / ctx-wins / ExitError extraction block — med

- **Where:** `internal/exec/exec.go:83-125` vs `127-193`.
- **What:** Roughly 20 near-identical lines: setting `c.Cancel` (same closure), `c.WaitDelay` defaulting, "context cancellation wins" `ctx.Err()` short-circuit, `errors.As(&exitErr)` → build `*Error`, trailing "startup failure wraps with `%s: %w`".
- **Why it matters:** Any future change to the cancel discipline (e.g. a log-on-SIGKILL debug hook) has to land in two places; they have already diverged on the `*Error` shape (Interactive's doesn't populate Stderr/Stdout/FirstStderrLine, which is correct-by-design but easy to forget).
- **Suggested fix:** Factor the common pieces into two small helpers:
  - `applyCancelAndWaitDelay(c *osexec.Cmd, waitDelay time.Duration)` — sets `c.Cancel` + default WaitDelay.
  - `finishRun(ctx, name, args, runErr) error` — ctx-wins + `errors.As` + build typed `*Error` (with an optional `Stderr/Stdout` closure for the buffering case).
  This is worth doing because it's on the hot path for every external process drift spawns.

### F8. `SSHTransportArgs`: per-call `os.Getenv("DRIFT_DEBUG")` and env prepend — low

- **Where:** `internal/rpc/client/client.go:140-172`.
- **What:** `runSSHRPC` calls `os.Getenv("DRIFT_DEBUG")` twice per RPC (one for the `env LAKITU_DEBUG=1` prefix, one for mirroring stderr). The `append([]string{"env", "LAKITU_DEBUG=1"}, remote...)` also allocates a fresh slice every call.
- **Why it matters:** Every drift subcommand that talks to a circuit goes through here, including `drift list` loops. The envvar does not change between calls in a single drift process.
- **Suggested fix:** Read `DRIFT_DEBUG` once at package init or at `New()`, store on the `Client`. Cache the `env LAKITU_DEBUG=1 lakitu rpc` argv as a package-level var.

### F9. Low-severity cleanups batched — low

Ten nits worth noting, none individually worth a finding:
1. `slogfmt.styleLevel` (`slogfmt.go:108-119`) takes both `level` and `padded` — the caller already has `level`; helper could be `styleLevel(p, padded)` and `switch strings.TrimSpace(padded)`. Minor.
2. `rpcerr.Error.Wire` (`rpcerr.go:128-133`) silently drops `json.Marshal(data)` errors; the data values come from `.With(k, v)` calls that could theoretically contain unmarshalable values. Log or fall back to `fmt.Sprintf("%v", v)` on failure, or panic-in-dev. Same issue in `MarshalJSON` at `rpcerr.go:114` (returns `nil, err`, which is correct, but inconsistent with `Wire`).
3. `rpcerr.Error.Error()` (`rpcerr.go:73-81`) produces `type: msg: cause` — when a caller does `rpcerr.Internal("%v", err).Wrap(err)` (very common: 40+ sites), the output is `internal_error: <err>: <err>` (doubled cause). Consider either dropping the `%v` or not setting `Cause` in that idiom.
4. `wire.validateRequest` (`wire.go:119-124`) calls `bytes.TrimSpace` unconditionally when params are present; the trim allocates. `json.Decoder` already parsed it, so you can check the first non-space byte by walking the raw slice manually, or just rely on the fact that Decoder would have rejected anything non-JSON.
5. `parseSemver` (`compat.go:98-121`) loops `for _, sep := range []string{"+", "-"}` and drops the first occurrence of each — fine for "1.2.3-rc+meta" but not for "1.2.3+meta-x" (drops `+meta-x`, leaves nothing). Unreachable because of `compat` being dead (F1), but if F1 is "wire it up", fix parsing order: drop `+` first then `-`, which is the semver.org spec. (Actually the current code's order happens to be `+` first then `-`, which is correct — so skip; this is a false alarm, but leaving the note so a reviewer can verify.)
6. `slogfmt.Emit` (`slogfmt.go:97-102`) allocates a keys slice + sorts every record. For servers under log pressure, a pre-sized slice reused via `sync.Pool` would be cheaper; but slog on drift is modest so probably not worth it.
7. `rpcerr.FromWire` (`rpcerr.go:137-157`) silently drops `json.Unmarshal(we.Data, &data)` errors. If the server ever sends malformed data, the client strips the `type` field and the user sees a plain `Error` with no classification. Prefer returning the original `we.Message` plus an internal warning, but low-risk in practice.
8. `exec.StderrTail` / `StdoutTail` (`exec.go:200-217`) walk `tailBytes` twice when both are attached — ANSI regex + redaction runs twice on sites that `.With` both. Consider combining or caching.
9. `rpc.call` (`rpc.go:75-82`) panics are recovered to `rpcerr.Internal("handler panic: %v", r)` but the stack trace is lost. Under a server process, log `runtime/debug.Stack()` to stderr before swallowing.
10. `version.Get` (`version.go:61`) exposes all four fields as a copy-by-value struct with public fields AND has a `Version` / `Commit` / `Date` / `APISchema` package-level var pair of the same names — two parallel sources of truth. Consider making the package vars unexported and exposing only `Get()`.

### Nothing to flag

- `internal/version/` — tiny, idiomatic `sync.OnceValue` wrapping `debug.ReadBuildInfo`. Clean.
- `internal/exec/termux.go` — complex but necessary; well-tested, well-commented, single entry point `termuxLinkerWrap`. No obvious simplification without regressing the `mosh`/`perl` shebang path coverage.
- `internal/exec/RedactingWriter` — line-buffered redaction is genuinely non-trivial and tightly tested.
- `internal/wire/methods.go` — the `const` block + `Methods()` slice duplication is called out in a comment and is the right tradeoff (compile-time constants on both sides, single slice for catalog consumers).

## Cross-refs

- F2's `cmd/lakitu/main.go:22-38` sits under chunk 6 (lakitu CLI) — fix lives here since the caller-side code is ≤5 lines and the primary change is in `rpcerr`.
- F1's "wire in the check" lands in chunks 1 (drift CLI, `deps.go`) and 2 (unchanged) if kept. Flag in both chunks.
- F7's helper extraction stays inside this chunk.
