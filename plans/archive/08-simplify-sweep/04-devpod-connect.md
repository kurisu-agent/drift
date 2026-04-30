# Simplify sweep — Devpod + connect + warmup

**Paths reviewed:** `internal/devpod/`, `internal/connect/`, `internal/sshconf/`, `internal/systemd/`, `internal/warmup/`
**Agent:** Claude Opus 4.7 (1M)

## Summary

- `internal/connect` re-types kart status as bare strings (`"running"`, `"stopped"`, `"busy"`) despite `devpod.Status` already existing as a typed enum used server-side on the same wire shape — highest-leverage cleanup.
- The 4-step SSH setup incantation (`EnsureInclude` → `EnsureSocketsDir` → `WriteCircuitBlock` → `EnsureWildcardBlock`) is copy-pasted across three CLI call sites; should collapse into one `sshconf.Manager` method.
- `sshconf.splitHostPort` reimplements `internal/name.SplitHostPort` with a one-line behavior delta (preserves IPv6 brackets); better to reuse `name.SplitHostPort` and reattach brackets at one call-site than keep two near-duplicates.
- `warmup.runSummary` and `listCharactersFor` probe each circuit **sequentially**; three circuits with a slow server add their RPC latencies together (and the default auto-start timeout is 30s).
- Several smaller cleanups: `trimJSONSpace` reimplements `bytes.TrimSpace`; `InstallDotfiles` wrapper only exists for one test; `osEnviron` var is a test seam that no test uses.

## Findings

### F1. `connect` uses stringly-typed kart status — med

- **Where:** `internal/connect/connect.go:166-184, 188-190, 219-222`
- **What:** `ensureRunning`/`pollUntilRunning` compare `info.Status` to bare string literals `"running"`, `"stopped"`, `"busy"`. The `InfoResult` struct declares `Status string \`json:"status"\``.
- **Why it matters:** `internal/devpod/status.go` already defines `type Status string` with `StatusRunning`/`StatusStopped`/`StatusBusy`/`StatusError`/`StatusNotFound`. `internal/server/kart.go:75` and `kart_lifecycle.go:57` already serialise via `devpod.Status` on the same wire method (`kart.info`). `connect` is the one unaware consumer, so a server-side rename/split of the enum would silently break auto-start branching here instead of failing compile.
- **Suggested fix:** change `InfoResult.Status` to `devpod.Status` and switch-arm on the constants. `default:` still catches unknown states. Drops six string-literal comparisons.
- **Cross-ref:** none — `devpod.Status` lives inside this chunk.

### F2. 4-step SSH setup sequence duplicated across three CLI sites — med

- **Where:** `internal/sshconf/sshconf.go:295-353` (helpers); callers in `internal/cli/drift/init.go:48-57`, `internal/cli/drift/circuit.go:120-131`, `internal/cli/drift/circuit.go:346-363`
- **What:** Every "add circuit" path calls the same four methods in order: `EnsureInclude(userPath)`, `EnsureSocketsDir()`, `WriteCircuitBlock(name, host, user)`, `EnsureWildcardBlock()`. The rename path in `circuit.go` skips `EnsureInclude`/`EnsureSocketsDir` but still does the last three. A future step added to the sequence (e.g. `EnsureKnownHosts`) has to land in three places.
- **Why it matters:** This is the prime spot for the repo's "forgot to call one of the four" bug, and the CLI deps plumbing (`sshManagerFor`, `userSSHConfigPath`) repeats too.
- **Suggested fix:** add `sshconf.Manager.InstallCircuit(userSSHConfigPath, circuit, host, user) error` that fans to the four calls internally. The rename path becomes `RemoveCircuitBlock(old) + InstallCircuit(...)`.
- **Cross-ref:** Chunk 1 (drift CLI) holds the three duplicate call sites; note there rather than duplicate.

### F3. `sshconf.splitHostPort` duplicates `name.SplitHostPort` — low

- **Where:** `internal/sshconf/sshconf.go:379-402` vs `internal/name/name.go:81-110`
- **What:** Two host:port splitters. `name.SplitHostPort` strips IPv6 brackets (`[::1]:22` → `("::1", "22")`); `sshconf.splitHostPort` preserves them (`[::1]:22` → `("[::1]", "22")`) because OpenSSH's `HostName` directive needs brackets back. The file comment says "so sshconf doesn't import internal/name" — but `name` already has no non-stdlib deps beyond `rpcerr`.
- **Why it matters:** Two bracket-handling rules drifting apart is exactly how IPv6 bugs land.
- **Suggested fix:** delete `sshconf.splitHostPort`; call `name.SplitHostPort` and, when the original `host` started with `[`, rewrap the hostname with brackets before emitting the `HostName` directive. Keeps one IPv6 parser.

### F4. `runSummary` probes circuits sequentially — med

- **Where:** `internal/warmup/warmup.go:415-434` and `listCharactersFor` at 443-463
- **What:** The summary loop does `deps.Probe(ctx, n)` and `listCharactersFor(ctx, deps, w, n)` in a serial `for n := range names`. Each `Probe` is a real SSH round-trip, and `listCharactersFor` is a second RPC per circuit. N circuits = 2N sequential RPCs.
- **Why it matters:** `drift init` summary on even three slow circuits is noticeably laggy; the worst case trips the 30s auto-start default (F1) if a circuit is momentarily stopped.
- **Suggested fix:** fan the N circuits out with `errgroup.Group.SetLimit(4)`; each goroutine runs (probe, character-list) in sequence (serial within a circuit, parallel across). Collect results into a pre-sized slice keyed by circuit name, then print in the existing `sortedKeys` order so output stays deterministic.

### F5. `trimJSONSpace` reimplements `bytes.TrimSpace` — low

- **Where:** `internal/devpod/devpod.go:520-537`; callers at 365, 428
- **What:** A hand-rolled whitespace trimmer that skips `' '`, `'\t'`, `'\n'`, `'\r'`. `bytes.TrimSpace` handles exactly those plus `'\v'`/`'\f'` which never appear in devpod JSON output anyway.
- **Why it matters:** Dead code surface; the function has no tests.
- **Suggested fix:** inline `bytes.TrimSpace(res.Stdout)` and delete `trimJSONSpace`.

### F6. `InstallDotfiles` exists only as a wrapper for one test — low

- **Where:** `internal/devpod/devpod.go:485-487` (one-liner), caller `internal/devpod/devpod_test.go:211`; production uses `InstallDotfilesWithOpts` at `internal/kart/new.go:213`.
- **What:** `InstallDotfiles(ctx, url)` calls `InstallDotfilesWithOpts(ctx, InstallDotfilesOpts{URL: url})`. No prod caller.
- **Suggested fix:** delete the wrapper; migrate the test to `InstallDotfilesWithOpts`.

### F7. `osEnviron` package-level var is dead test seam — low

- **Where:** `internal/devpod/devpod.go:20-22`; referenced at `devpod.go:152, 504`
- **What:** Comment claims "tests that stub the runner don't need to manipulate the real process env". No test in `internal/devpod/` writes to `osEnviron`; `TestClientDevpodHomeInjectsEnv` exercises the real `os.Environ()` via `EnvOrNilForTest`.
- **Why it matters:** Mutable package-level state that nothing actually swaps is an invitation to race bugs the day someone does mutate it in a parallel test.
- **Suggested fix:** call `os.Environ()` directly at the two sites; delete the var.

### F8. `InstallDotfilesWithOpts` replicates `run`'s mirror/echo plumbing — low

- **Where:** `internal/devpod/devpod.go:494-518`
- **What:** The method bypasses `c.run(ctx, args...)` to allow per-call env layering, so it re-inlines `c.echoArgv`, `c.runner().Run`, `c.streamMirror()` twice. A future addition to `c.run` (e.g. a new mirror type) has to land twice.
- **Suggested fix:** extract a private `c.runWithEnv(ctx, env, args)` that both `run` and `InstallDotfilesWithOpts` call, or give `c.run` an optional `extraEnv []string` parameter. Single point of truth for spawn plumbing.

### F9. `EnsureSocketsDir` double-chmods a just-created directory — low

- **Where:** `internal/sshconf/sshconf.go:335-353`
- **What:** `os.MkdirAll(path, 0o700)` followed unconditionally by `os.Chmod(path, 0o700)`. The chmod only matters when the dir pre-exists with looser perms (the comment says so); on first-create it's a wasted syscall.
- **Suggested fix:** stat first; only chmod if the dir existed and perm ≠ 0o700. Or accept the one-syscall cost — it's hot-path-adjacent, not hot-path.

### F10. `moshAvailable` and `Transport` duplicate the mosh-detection branch — low

- **Where:** `internal/connect/connect.go:111-117` (`moshAvailable`) vs `125-136` (`Transport`)
- **What:** Both functions have a nil-guard on `LookPath` and swallow `LookPath` errors identically. `Transport` additionally handles `ForceSSH`.
- **Suggested fix:** make `moshAvailable` call `Transport(d.LookPath, opts.ForceSSH) == "mosh"`, or delete `moshAvailable` and have `Run` call `Transport` directly. Picks up the ForceSSH short-circuit for free on the one caller.

### F11. `parseManaged` scanner allocates a large buffer per call — low

- **Where:** `internal/sshconf/sshconf.go:77-78, 324-325`
- **What:** Each parse builds a 1MiB scanner buffer (`scanner.Buffer(..., 1<<20)`). `WriteCircuitBlock`, `RemoveCircuitBlock`, `EnsureWildcardBlock`, `EnsureInclude` all parse-then-write, and `ListCircuits` parses too. Typical managed SSH configs are <2KiB.
- **Suggested fix:** drop the oversized buffer call; default `bufio.Scanner` buffer (64KiB) is more than enough for any realistic ssh_config slice. If paranoia wants a ceiling, 64KiB is already the scanner max line length.

## Nothing to flag

- `internal/devpod/bootstrap.go`: download-and-verify logic is tight; tempfile→sha→rename is atomic and handles concurrent callers correctly. No unreachable error paths.
- `internal/systemd/systemd.go`: the `Client` is a genuinely small wrapper with no copy-paste. `looksLikeDenial`'s substring list is narrow-by-design per its comment.
- `internal/exec` usage: every shell-out in this chunk (`devpod.Client.run`, `InstallDotfilesWithOpts`, `connect.execStdio`, `systemd.Client.run`) routes through `internal/exec` — the Termux/`os.Executable`/linker-wrap contract from `CLAUDE.md` is honored. `connect.go` and `devpod.go` import `os/exec` only for `ErrNotFound` and `LookPath`, not for spawning.
- `internal/devpod/agent_workspace.go`: tolerant JSON parsing and silent skip on corrupt entries is intentional per the migrate-use-case.
- No TOCTOU in `EnsurePinned`: dest is read then atomically replaced; concurrent callers write distinct tempfiles.
