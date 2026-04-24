# Simplify sweep — Integration tests

**Paths reviewed:** `integration/harness.go`, `integration/*_test.go` (13 files), `integration/shim/devpod/main.go`, `integration/Dockerfile.circuit`
**Agent:** opus-4.7 (1M context)

## Summary

- Every test rebuilds both `drift` and `lakitu` via `go build` and then reruns `docker build` of the circuit image — huge sequential cost on a suite that already takes ~2 min. One-time build per `go test` process would recover most of the minutes.
- `StartCircuit` + `lakitu init` + `RegisterCircuit(ctx, "test")` is repeated inline in 13 tests. The three-line stanza should be a single harness helper (`StartReadyCircuit`), and `setupTuneCircuit` should fold into it rather than living as a local `tune_test.go` helper that `dotfiles_test.go` and `env_test.go` then cross-import.
- `stageLocalStarter` in `tune_test.go` duplicates ~25 lines of `Circuit.StageStarter`; the one in the harness is strictly more capable. Delete the local copy and use the method.
- `harness.go` at 924 LOC is the largest file in the repo and mixes five orthogonal concerns (docker lifecycle, ssh config, CLI shell-outs, devpod shim/recorder, generic utils). One big flag, not thirty nits — split when touched.
- `runtime.GOARCH` is appended then immediately overwritten by `"amd64"` in `buildImage` for non-linux hosts — the first append is dead, and the second is wrong for Apple Silicon hosts building for a `linux/arm64` container (it'd force x86 emulation).

## Findings

### F1. Every test rebuilds drift + lakitu + docker image from scratch — med

- **Where:** `integration/harness.go:56-102` (`StartCircuit` → `buildImage`) and `integration/harness.go:891-901` (`driftBinary`). `driftBinary` is called once per test via `Drift()` and once more via `SSH()`/`DriftBinDir()`; each call does `go build -o <t.TempDir()>/drift ./cmd/drift`. `buildImage` does `go build ./cmd/lakitu` + `docker build`.
- **What:** With 18 tests (`integration/*_test.go`), a cold run sequentially does 18 `go build drift`, 18 `go build lakitu`, and 18 `docker build drift-integration-circuit` — the image tag is identical, so all 17 trailing builds are pure cache hits on the content layers but still a second each for daemon RTT. The drift/lakitu builds are not cached across tests because they go to per-test `t.TempDir()`.
- **Why it matters:** Makes `make integration-test` noticeably slower than it needs to be, and scales linearly with test count. Not a correctness risk, but it's the biggest single efficiency win in this chunk.
- **Suggested fix:** Memoise both binaries at package scope using `sync.Once` (keyed on the caller's repo root). `TestMain` is the canonical spot: build once into a package-scoped tmpdir, have `driftBinary` and `buildImage` read from it. `go build` itself already dedupes via the build cache, but writing to the same destination path a second time still pays for link+write — memoising bypasses that too.

### F2. `StartCircuit` + `lakitu init` + `RegisterCircuit("test")` repeated in 13 tests — med

- **Where:** The pattern `integration.StartCircuit(...)` → `integration.SSHCommand(ctx, c, "lakitu", "init")` → `c.RegisterCircuit(ctx, "test")` is duplicated at `character_test.go:25-28`, `chest_test.go:27-30`, `connect_test.go:31-35`, `drift_test.go:22-34` (inlined variant), `probe_test.go:23-29` (variant), `realdevpod_clone_test.go:41-45`, `realdevpod_test.go:35-39`, `run_test.go:26-28, 77-80, 100-104`, `ssh_proxy_test.go:30-34`, and absorbed via `setupTuneCircuit` (`tune_test.go:41-49`, called from 5 tune tests + `env_test.go` + `dotfiles_test.go`).
- **What:** Every caller writes the same three lines with the same error handling. `setupTuneCircuit` already captures the pattern but is (a) named for "tune" and (b) bundled with `InstallDevpodRecorder`, so tests that don't need the recorder either copy the stanza inline or pay for the recorder install anyway.
- **Why it matters:** Every new integration test has to copy three-to-four lines that nothing about the test itself varies. Refactors (e.g. changing the circuit name, swapping `lakitu init` for something else) have to touch 13 sites.
- **Suggested fix:** Promote a harness-level `StartReadyCircuit(ctx, t, opts...)` returning `(*Circuit, *DevpodRecorder)` where recorder is opt-in. Move it next to `StartCircuit`; delete `setupTuneCircuit` and `stageLocalStarter` from `tune_test.go`; update call sites to the new helper. Tests that need the "bare" circuit without `lakitu init` / registration (just `drift_test.go:18` and `probe_test.go:19` — both are testing the registration flow itself) stay on `StartCircuit`.

### F3. `stageLocalStarter` duplicates `Circuit.StageStarter` — med

- **Where:** `integration/tune_test.go:51-76` reimplements a ~22-line git-init-and-bare-clone script inside the circuit that is functionally a subset of `integration/harness.go:455-484` (`Circuit.StageStarter`).
- **What:** The local helper hardcodes a single `README.md` with body `# starter\n`. `Circuit.StageStarter` accepts an arbitrary `map[string]string` and would cover this by passing `{"README.md": "# starter\n"}`. The only meaningful difference is that `stageLocalStarter` uses `&&`-joined commands instead of `set -e`, which is irrelevant.
- **Why it matters:** Two copies of shell-in-ssh git scaffolding to maintain. One already drifted: `StageStarter` uses heredocs (survives quote/backtick content), `stageLocalStarter` uses `echo` (would break on anything fancy).
- **Suggested fix:** Replace the `stageLocalStarter` call at `tune_test.go:87` with `c.StageStarter(ctx, "starterA", map[string]string{"README.md": "# starter\n"})`; delete the local helper.

### F4. `harness.go` is 924 LOC and mixes five unrelated concerns — med

- **Where:** `integration/harness.go`.
- **What:** One file contains (a) container lifecycle / docker run args / sshd + auth bootstrap [L56-315], (b) SSH config + shim generation [L331-450, L702-738], (c) git-repo staging helpers [L452-525], (d) devpod shim + recorder [L527-640], (e) generic utilities (`overlayEnv`, `freePort`, `copyFile`, `randomHex`, `asExitError`, `run`, `driftBinary`, `repoRoot`) [L786-924]. Nothing here is logically part of a single "harness" except that `Circuit` methods all take `*Circuit` as the receiver.
- **Why it matters:** It's the largest file in the repo. `Circuit` has grown a bag-of-stuff quality: eight unexported fields, three kinds of path, and 20+ methods. Every new test helper lands here by default.
- **Suggested fix:** Flag for a follow-up refactor, don't gold-plate now. Natural splits, keeping everything in package `integration`:
  - `harness_container.go` — `StartCircuit`, `buildImage`, `runContainer`, `waitForSSH`, `Stop`, `sweepIntegrationContainers`.
  - `harness_ssh.go` — `writeSSHConfig`, `writeSSHShim`, `SSH`, `SSHCommand`, `RegisterCircuit`.
  - `harness_shims.go` — `InstallDevpodShim`, `InstallBin`, `InstallDevpodRecorder`, `DevpodRecorder` + `DevpodInvocation`, `ReadArtifact`, `ListArtifact`.
  - `harness_git.go` — `StageStarter`, `StageCloneFixture`.
  - `harness_util.go` — `overlayEnv`, `freePort`, `copyFile`, `randomHex`, `asExitError`, `run`, `driftBinary`, `repoRoot`, `dockerSocketGID`.

### F5. `buildImage` has dead + wrong GOARCH handling — med

- **Where:** `integration/harness.go:163-168`.
- **What:**
  ```go
  build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH)
  if runtime.GOOS != "linux" {
      build.Env = append(build.Env, "GOARCH=amd64")
  }
  ```
  Two issues: (1) Go's exec treats duplicate env keys unpredictably — libc `getenv` returns the first match, but Go slurps the last one for `Cmd.Env`, so the amd64 override does take effect but only by accident of that ordering. The first `GOARCH=` is dead anyway on non-linux hosts. (2) The amd64 override is wrong for Apple Silicon contributors: `runtime.GOARCH` is `arm64`, the circuit image's `FROM debian:bookworm-slim` runs happily on arm64 linux, and hardcoding amd64 forces qemu emulation for the whole circuit run (slow + flaky).
- **Why it matters:** Reliability on non-amd64 mac hosts (the case the comment says this codepath is for). Also exactly the "stringly-typed env override" footgun `overlayEnv` at L843 was built to prevent — the harness applies it to docker but not to its own go build.
- **Suggested fix:** Replace the block with a single call path that uses `overlayEnv(map[string]string{"CGO_ENABLED": "0", "GOOS": "linux", "GOARCH": runtime.GOARCH})`. The cross-build that's actually wanted here is "build linux binaries for the same CPU the host's docker daemon runs on" — on linux that's `runtime.GOARCH`, on mac-with-Docker-Desktop it's also `runtime.GOARCH` (Docker Desktop on arm64 macs runs a linux/arm64 VM by default).

### F6. `RegisterCircuit` hand-appends an ssh_config block that mirrors `writeSSHConfig` — low

- **Where:** `integration/harness.go:752-784` appends a `Host drift.<name>` block with the same eight options (`HostName`, `Port`, `User`, `IdentityFile`, `IdentitiesOnly`, `StrictHostKeyChecking`, `UserKnownHostsFile`) already defined at `integration/harness.go:358-365` for `Match host "drift.*,!drift.*.*"`.
- **What:** The pattern match at L358 already matches `drift.<name>` for any name. The appended concrete `Host drift.test` block exists so `drift.test.mykart` (the three-dot form) resolves via `Host drift.*.*` through ProxyCommand while `drift.test` resolves via the two-dot form — but the two-dot form is already covered by the `Match host` block. The appended block is dead weight unless it's there to disambiguate something subtle.
- **Why it matters:** Reader confusion. The existing pattern-match block is claimed to cover this alias, and if the appended block is actually required (and the `Match` block isn't sufficient), the reason isn't in the comment.
- **Suggested fix:** Either (a) confirm the `Match host` block is sufficient and delete the append in `RegisterCircuit`, or (b) add a sentence to the `writeSSHConfig` comment explaining why per-circuit `Host drift.<name>` blocks are needed on top of the pattern match. Lean on (a) first — if `ssh_proxy_test.go` still passes after deletion, the append was redundant.

### F7. `chmod + docker cp + docker exec chmod` triplet copy-pasted across five install helpers — low

- **Where:** `integration/harness.go:530-542` (`InstallDevpodShim`), `544-556` (`InstallBin`), `577-591` (`InstallDevpodRecorder`), `261-280` (authorized_keys), `287-301` (ssh environment). Each writes a host file, `docker cp`s it into `/usr/local/bin/...` (or `.ssh/...`), then `docker exec chmod 0755` (or chown+chmod 0600).
- **What:** The three-line sequence is structurally identical; only the destination path and the file mode differ. `InstallDevpodShim` and `InstallDevpodRecorder` differ only in whether the body is the literal argument or a compiled binary.
- **Why it matters:** Five copies of the same docker-cp-and-chmod dance. A bug in error handling would need fixing in all five.
- **Suggested fix:** Extract one helper, e.g. `func (c *Circuit) installHostFile(ctx, src, dst string, mode os.FileMode, chown string) error`; have the three install helpers call it with the relevant parameters. Also folds the `chown` step for the auth/environment files.

### F8. `devcontainerIDs` / `setDiff` / `setDiffSet` / `workspaceContainerName` / `dockerExec` are test-file helpers that belong in the harness — low

- **Where:** `integration/realdevpod_test.go:108-132` defines `devcontainerIDs` + `setDiff`; `integration/realdevpod_clone_test.go:129-176` adds `setDiffSet`, `workspaceContainerName`, `dockerExec`.
- **What:** These are generic helpers (docker-ps wrappers and set operations) with zero test-specific logic. They live in test files because realdevpod_clone_test.go was added later and needed the same baseline/diff pattern.
- **Why it matters:** The test file defining `setDiff` can't import from the other test file, so `setDiffSet` exists mostly because a follow-up test couldn't reuse `setDiff`. Future devpod E2E tests will reinvent the same wheel.
- **Suggested fix:** Move all five to `harness.go` (or to the `harness_shims.go` split proposed in F4). Rename `setDiff`/`setDiffSet` to `diffIDs`/`diffIDsSet` or similar, drop the stringy/setty suffix split by always returning a `map[string]struct{}` and having callers that want a slice convert locally.

### F9. Every test spells out `context.WithTimeout(context.Background(), 5*time.Minute)` — low

- **Where:** 25 occurrences across every `_test.go` (see grep: `context.WithTimeout` in integration tests).
- **What:** Each test starts with the same four-line ctx boilerplate. Timeouts vary only between 2/5/6/10 minutes, and the 5-minute case dominates.
- **Why it matters:** Noise at the top of every test. Trivially fixable.
- **Suggested fix:** Helper `func testCtx(t *testing.T, d time.Duration) context.Context` that does `context.WithTimeout` + `t.Cleanup(cancel)`. Callers become `ctx := testCtx(t, 5*time.Minute)`.

### F10. Default circuit name "test" is hardcoded in 9 callers — low

- **Where:** Every `c.RegisterCircuit(ctx, "test")` call uses the literal string `"test"`. Same in `probe_test.go:28` and `drift_test.go:32` (`lakitu config set name test`).
- **What:** No test exercises a different name — `"test"` is the canonical circuit name for the suite.
- **Why it matters:** If we ever want to parallelise tests by having multiple named circuits in the same run, or flush out bugs where the name happens to equal `"test"` literally, the string needs changing in 9 places.
- **Suggested fix:** Either make it a package-level `const circuitName = "test"` the harness and tests both reference, or default the parameter in `RegisterCircuit` and have a separate `RegisterCircuitAs(name)` for the rare non-default case. The former is cheaper.

### F11. `stageLocalStarter` error-joins commands with ` && ` while `StageStarter` uses `set -e` — low

- **Where:** `integration/tune_test.go:59-71` vs `integration/harness.go:460-478`.
- **What:** Two ways to spell "fail on first error" in the same file. Cosmetic but confusing.
- **Why it matters:** Low-value consistency nit.
- **Suggested fix:** Subsumed by F3 (delete `stageLocalStarter`).

### Batched low-severity nits (one line each, no separate Fn)

- `integration/harness.go:865-871` `copyFile` reads entire file into memory; fine for ≤MB files but `io.Copy` would be cleaner and the 0o755 mode is hardcoded whether or not the source was executable.
- `integration/harness.go:843-863` `overlayEnv` is called via both `Drift()` and `SSH()` but not via `buildImage` — inconsistent (see F5).
- `integration/env_test.go:304-322` `argvHasValuePrefix` / `envHas` / `findAllUps` live in `env_test.go` but are generic; move to harness.
- `integration/tune_test.go:19-36` `argvHas` / `argvValue` live in `tune_test.go` but are used from `env_test.go` and `dotfiles_test.go`; move to harness.
- `integration/tune_test.go:344-364` `assertJSONEqual` + `jsonMismatch` is a tiny custom matcher used three times in one file; `encoding/json.RawMessage` + `reflect.DeepEqual` after `json.Unmarshal` into `any` would be one line.
- `integration/shim/devpod/main.go:64` bare `argv[0]` access panics if `os.Args` is empty; `sub` is computed defensively just above but the switch isn't.
- `integration/Dockerfile.circuit:20` pins `DEVPOD_VERSION=v0.22.0`; keep in sync with `internal/devpod/version.go` — currently no enforcement that the integration image matches what `EnsurePinned` would download.
- `integration/harness.go:132-134` `Target()` returns `user@127.0.0.1:port`; every caller then has to remember to pass it to `drift circuit add` — fine, but `Target()` could alternatively return a pre-built `ssh://` URL to eliminate one mental hop.

## Nothing to flag

- **Test fixtures mirror production.** `realdevpod_test.go` uses `debian:bookworm-slim`, `realdevpod_clone_test.go` uses `mcr.microsoft.com/devcontainers/base:debian`. The single `alpine:latest` reference in `tune_test.go:167` is fixture *content* (the test asserts its bytes round-trip through `--extra-devcontainer-path`) — not an image the test actually runs — so it doesn't violate the "mirror production" rule.
- **Sleep discipline.** Only one sleep-based wait (`waitForSSH`, `harness.go:317-329`) and it's condition-based with a deadline — not a flake vector.
- **Cleanup discipline.** `sweepIntegrationContainers` runs both pre-test (clean up crashed earlier runs) and post-test via `t.Cleanup`, filtered by `drift.integration=1` + runID labels. Good pattern, don't regress.
- **`-short` gating.** `realdevpod_test.go` and `realdevpod_clone_test.go` correctly skip under `-short`; `StartCircuit` itself also gates on `-short`.
- **Build tag consistency.** Every `_test.go` under `integration/` carries `//go:build integration` — no stray files get pulled into the fast `go test ./...` path.
