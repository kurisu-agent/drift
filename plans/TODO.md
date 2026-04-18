# drift — MVP development plan

Execution checklist derived from [PLAN.md](./PLAN.md). PLAN.md remains the
contract/spec; this file is the ordered punch list to MVP.

**MVP definition.** A user can: install lakitu on a Linux circuit, install
drift on a workstation, run `drift warmup` to register the circuit + a
character, run `drift new <name> --clone <url>`, and `drift connect <name>`
into a devcontainer over mosh. All commands in [CLI design](./PLAN.md#cli-design)
work end-to-end. Everything in [Future](./PLAN.md#future) is explicitly
out of scope.

Legend: `[x]` done · `[ ]` open · `[~]` partial.

---

## Phase 0 — Foundation

- [x] Go module + repo layout per [PLAN.md § Repo layout](./PLAN.md#repo-layout)
- [x] `internal/wire` — JSON-RPC 2.0 request/response/error types + decoder validation + fuzz target
- [x] `internal/rpcerr` — typed `*Error` with stable code/type, `errors.Is/As`, MarshalJSON
- [x] `internal/version` — ldflag receivers + `debug.ReadBuildInfo` fallback
- [x] `cmd/{drift,lakitu}/main.go` — signal-cancellable root context, lakitu panic recovery preserves stdout invariant
- [x] Kong CLI skeletons for both binaries
- [x] testscript harness (`internal/cliscript`) + smoke scripts
- [x] `.golangci.yml` v2 + `Makefile` (test/test-race/lint/vuln/fuzz/build)
- [x] `go vet`, `golangci-lint`, `govulncheck` — all green

---

## Phase 1 — RPC dispatch layer

- [x] `internal/rpc` — method registry: `Register(name string, handler func(ctx, params) (result, error))`
- [x] Dispatcher: parse `*wire.Request`, look up handler, marshal result or convert error to `*wire.Error` exactly once at the boundary ([PLAN.md § Error handling](./PLAN.md#error-handling))
- [x] Param decoding helper: typed struct binding via `json.Unmarshal` with `DisallowUnknownFields`
- [x] Wire `lakitu rpc` to use the dispatcher (replace the current method_not_found stub)
- [x] Stdout invariant test (testscript): every `lakitu` subcommand and `lakitu rpc` invocation that runs to completion produces ≤1 JSON object on stdout when invoked in RPC mode; no log lines leak
- [x] `internal/rpc/client` — drift-side helper: `Call(ctx, circuit, method, params, &result) error` that shells out to `ssh <alias> lakitu rpc`, distinguishes transport (exit 255) from RPC error
- [x] Shared method-name constants in `internal/wire` so client and server reference the same strings

---

## Phase 2 — Config layer

- [x] `internal/config` — YAML loader for both client and server configs (yaml.v3 + struct tags + `Validate()`); reject unknown keys
- [x] Client `~/.config/drift/config.yaml` schema + atomic write helper ([PLAN.md § Client config layout](./PLAN.md#client-config-layout))
- [x] Server `~/.drift/garage/config.yaml` schema ([PLAN.md § Server state layout](./PLAN.md#server-state-layout))
- [x] `lakitu init` — idempotent garage bootstrap (creates `~/.drift/garage/{tunes,characters,chest,karts}` with default `config.yaml`)
- [x] Path resolution honors `XDG_CONFIG_HOME` on the client and `$HOME` on the server; testscript covers both

---

## Phase 3 — SSH config management

- [x] `internal/sshconf` — parser/writer for `~/.config/drift/ssh_config` blocks
- [x] `internal/sshconf` — idempotent insert of `Include ~/.config/drift/ssh_config` at top of `~/.ssh/config` (creates 0600 if absent, never edits other lines)
- [x] Per-circuit block writer with full ControlMaster stanza ([PLAN.md § Generated Host blocks](./PLAN.md#generated-host-blocks))
- [x] Per-kart wildcard block (`Host drift.*.*`) — appended once at end of managed file
- [x] `~/.config/drift/sockets/` directory created with mode 0700
- [x] `manage_ssh_config: false` short-circuits all writes
- [x] Testscript: add → re-add → rm sequence is fully idempotent and leaves `~/.ssh/config` unchanged after rm (covered by `TestAddThenRmRestoresUserSSHConfigByteIdentical` — a tempdir-based Go test; no txtar driver since Phase 3 is library-only and cliscript.go is out of scope)

---

## Phase 4 — Circuit management (client)

- [x] `drift circuit add <name>` — flags `--host`, `--default`, `--no-ssh-config`; updates client config + SSH config
- [x] `drift circuit rm <name>` — config + SSH block removal; preserves `Include` line
- [x] `drift circuit list` — table output; JSON via global `--output json`
- [x] Kart-name regex validator (`^[a-z][a-z0-9-]{0,62}$`) shared between client and server; reserved names `default`, `none`
- [x] Probe step: `server.version` RPC, surface latency + version on success

---

## Phase 5 — `internal/exec` external-process wrapper

- [x] Single helper around `exec.CommandContext` that always sets `Cancel` (SIGTERM) and `WaitDelay` (5s → SIGKILL) per [PLAN.md § Critical invariants](./PLAN.md#critical-invariants-mechanically-tested)
- [x] Never invoke a shell — argv built directly; unit test asserts the package itself never bakes in a shell invocation
- [x] Capture stdout/stderr separately; structured error with exit code + first stderr line
- [x] Used uniformly by ssh, docker, devpod, git call sites (`internal/rpc/client.SSHTransport`, `internal/devpod`, `internal/systemd`, `internal/kart.Starter` all route through `driftexec.Run`). Interactive stdio paths (`internal/connect.execStdio`, `internal/cli/drift.execSSHProxy`) deliberately bypass `internal/exec` because it buffers stdio — they reproduce the Cancel/WaitDelay discipline inline and are documented as such.

---

## Phase 6 — Method handlers (server-side, devpod-free first)

Order matters: trivial handlers first to validate the dispatch path end-to-end before the devpod integration lands.

- [x] `server.version` / `lakitu version` — wired through dispatcher; semver compat helper in `internal/rpc/client` ([PLAN.md § Version compatibility](./PLAN.md#version-compatibility))
- [x] `server.init` / `lakitu init` (Phase 2 — verified registered)
- [x] `config.show` / `config.set` — server-level config get/set with key validation
- [x] `character.add|list|show|remove` — file-backed under `~/.drift/garage/characters/<name>.yaml`; `pat_secret` must be `chest:<name>` form, literals rejected ([PLAN.md § Character file](./PLAN.md#character-file-charactersnameyaml))
- [x] `tune.list|show|set|remove` — file-backed under `~/.drift/garage/tunes/<name>.yaml`; reject removal if any kart references the tune
- [x] `chest.set|get|list|rm` — `ChestBackend` interface + `yamlfile` backend writing `~/.drift/garage/chest/secrets.yaml` (mode 0600, top-level `name: value` map with block scalars for multi-line values); set value piped via stdin
- [x] Semver compat check in drift: `internal/rpc/client.CompatChecker` caches `server.version` per circuit; major→error, minor→warn, patch→silent; `--skip-version-check` bypasses (wiring into remote subcommands lands with Phase 9+)

---

## Phase 7 — devpod integration

- [x] `internal/devpod` — typed wrapper over devpod CLI: `Up`, `Stop`, `Delete`, `Status`, `SSH`, `List`, `Logs`, `InstallDotfiles` ([PLAN.md § devpod integration](./PLAN.md#devpod-integration))
- [x] All calls go through `internal/exec` with context cancellation
- [x] `kart.list` — surface `devpod list --output json` plus garage state
- [x] `kart.info` — JSON shape per [PLAN.md § lakitu info schema](./PLAN.md#lakitu-info-kart--json-schema); status enum `running|stopped|busy|error|not_found`
- [x] Stale-kart detection: garage dir without devpod workspace → `code:4 stale_kart` ([PLAN.md § Stale karts](./PLAN.md#stale-karts))

---

## Phase 8 — Kart creation + flag composition

- [x] Flag resolution per [PLAN.md § Flag composition](./PLAN.md#flag-composition-and-resolution): server defaults → tune → explicit flags; `--features` always additive
- [x] `--devcontainer` accepts file path, JSON string, or URL — last two written to temp file
- [x] Starter history strip ([PLAN.md § Starter history strip](./PLAN.md#starter-history-strip)): clone → rm `.git` → re-init → initial commit using active character (fallback `drift <noreply@drift.local>`)
- [x] Layer-1 dotfiles generator from active character (gitconfig, gh hosts.yml, credential helper, optional ssh key + entry) ([PLAN.md § Dotfiles injection](./PLAN.md#dotfiles-injection))
- [x] Layer-2 dotfiles passed through as `devpod up --dotfiles <url>`
- [x] `kart.new` handler ties it all together; rejects existing-name with `code:4 name_collision`
- [x] Interrupt cleanup ([PLAN.md § Interrupts](./PLAN.md#interrupts)): cancel in-flight devpod, remove tmpdirs, write `status: error` marker if kart dir already exists

---

## Phase 9 — Kart lifecycle handlers

- [x] `kart.start` (idempotent) — `devpod up <name>`
- [x] `kart.stop` (idempotent) — `devpod stop <name>`
- [x] `kart.restart`
- [x] `kart.delete` — errors `code:3 kart_not_found` on missing
- [x] `kart.logs` — chunk return; streaming deferred ([Future](./PLAN.md#future))
- [x] Drift-side commands wired through RPC for each above

---

## Phase 10 — `drift connect`

- [x] Detect mosh availability via `exec.LookPath("mosh")`; default to mosh, fall back to `ssh -t`
- [x] Auto-start kart if status is `stopped` via `kart.info` → `kart.start` → poll `kart.info` until `running` (30s budget)
- [x] `--ssh` flag forces ssh; `--forward-agent` enables `-A`
- [x] Use the managed `drift.<circuit>` alias as the SSH/mosh target; final command is `devpod ssh <kart>`; interactive stdio wired through with the Cancel/WaitDelay discipline from plans/PLAN.md § "Critical invariants"

---

## Phase 11 — Per-kart SSH proxy

- [x] `drift ssh-proxy <alias> <port>` subcommand — parses `drift.<circuit>.<kart>`, opens `ssh drift.<circuit> devpod ssh <kart> --stdio`, pipes stdio ([PLAN.md § How drift ssh-proxy works](./PLAN.md#how-drift-ssh-proxy-works))
- [x] Wildcard `Host drift.*.*` block auto-written by Phase 3 already exercises this path (verified: `internal/sshconf.EnsureWildcardBlock` emits `ProxyCommand drift ssh-proxy %h %p`)
- [x] Smoke test from inside the integration harness: `ssh drift.<circuit>.<kart> echo ok` — `TestSSHProxyEchoOK` in `integration/ssh_proxy_test.go`. The circuit image lacks a docker daemon, so the test swaps `/usr/local/bin/devpod` with a shim that bridges `devpod ssh <kart> --stdio` to local sshd via netcat. The outer SSH client completes its handshake through the ProxyCommand pipe and runs `echo ok`, exercising drift ssh-proxy + the nested ssh hop for real.

---

## Phase 12 — Auto-start (systemd)

- [x] `lakitu-kart@.service` template unit at `packaging/systemd/lakitu-kart@.service`
- [x] `kart.enable` / `kart.disable` handlers shell out to `systemctl --user enable|disable --now lakitu-kart@<name>` via `internal/systemd.Client`; idempotent; drift-side `drift enable` / `drift disable` subcommands route through RPC
- [x] `loginctl enable-linger <user>` documented in install path — Phase 17 README covers it; surfaced to the user automatically via `code:6 systemd_denied` when the user bus can't be reached
- [x] `autostart` marker file at `~/.drift/garage/karts/<name>/autostart` written on enable, removed on disable (mode 0600); reconciliation during `lakitu init` deferred — explicit init-time sync can't drive systemctl safely because the user bus isn't always up, so marker is treated as truth at kart-lifecycle time and systemd is re-checked lazily

---

## Phase 13 — `drift warmup`

- [x] Interactive wizard ([PLAN.md § drift warmup](./PLAN.md#drift-warmup)): circuits → characters → summary
- [x] Re-runnable; each phase skippable (`--skip-circuits`, `--skip-characters`, `--no-probe`)
- [x] Detects non-TTY stdin and returns `code:2 user_error`
- [x] Probe uses Phase 1 RPC client; install hints printed on failure

---

## Phase 14 — Human CLI error formatting

- [x] stderr format ([PLAN.md § stderr format](./PLAN.md#stderr-format-human-cli-path)): line 1 `error: <message>`, line 2 single-line JSON of the error object; exit code mirrors `Code` — implemented by `internal/cli/errfmt.Emit`; drift + lakitu CLIs refactored to route through it
- [~] Idempotency contract verified per verb ([PLAN.md § Idempotency](./PLAN.md#idempotency)) — verified by `internal/server/kart_lifecycle_test.go` for start/stop/restart/delete; enable/disable pending until Phase 12 lands
- [x] Unit tests assert the two-line format + exit code for every error code category (2 user, 3 not_found, 4 conflict, 5 devpod, 6 auth) in `internal/cli/errfmt/errfmt_test.go`; testscript-level golden tests deferred — unit coverage is stricter and easier to maintain

---

## Phase 15 — Integration harness (tier-2 tests)

- [x] Dockerfile for a "circuit" image at `integration/Dockerfile.circuit`: Debian-slim + sshd + devpod + lakitu (docker access is delegated to the devcontainer's outer daemon via socket bind, matching plans/PLAN.md § "Integration harness")
- [x] Test driver at `integration/harness.go`: builds the image, spins a per-test container on an ephemeral port, generates an ed25519 keypair, writes a per-test ssh config, and exposes `Circuit.Drift(ctx, args...)` so tests drive the real `drift` binary over real SSH
- [x] Build-tag-gated (`//go:build integration`) so unit `go test ./...` stays fast
- [~] Cover: warmup probe, kart.new with `--clone`, connect via ssh fallback, kart.delete, character add+list, chest set+get. Done in `integration/`: init+version (`TestDriftInitAndServerVersion`), probe via circuit add (`TestCircuitAddProbe`), character add/list/show/remove (`TestCharacterLifecycle`), chest set/get/list/rm incl. multiline values (`TestChestLifecycle`), and end-to-end ssh-proxy smoke (`TestSSHProxyEchoOK`). Kart lifecycle (`kart.new --clone`, `drift connect`, `kart.delete`) stays deferred — the circuit image has no docker daemon so `devpod up` cannot actually stand up a workspace; unit-level coverage in `internal/server` + `internal/kart` remains the tier-1 net.
- [x] CI job target in `Makefile`: `make integration`

---

## Phase 16 — Release artifacts

- [x] `.goreleaser.yaml` — `CGO_ENABLED=0`, `-trimpath`, `mod_timestamp: {{.CommitTimestamp}}`, ldflags injecting `internal/version.{Version,Commit,Date}`
- [x] Build matrix: drift {linux,darwin}×{amd64,arm64}; lakitu linux×{amd64,arm64}
- [x] `flake.nix` — devShell + manual binary install path documented
- [x] `.github/workflows/ci.yml` — vet, test-race, lint, vuln, govulncheck weekly cron on `main`

---

## Phase 17 — Bootstrap docs

- [x] README quickstart: install lakitu (manual tarball), `lakitu init`, install drift, `drift warmup`, `drift new`, `drift connect`
- [x] Manual-install checklist mirrors what the (future) Nix module would automate ([PLAN.md § Bootstrap / install](./PLAN.md#bootstrap--install))
- [x] Document `--skip-version-check` use during upgrades

---

## Explicitly out of MVP scope

Tracked here for "no, that's later" answers. See [PLAN.md § Future](./PLAN.md#future).

- Ports management (`drift ports`, conflict detection, per-workstation remap persistence)
- `lakitu serve` long-lived stdin/stdout RPC with batching/streaming notifications
- Cross-circuit sync of characters/tunes/chest
- Chest backends beyond `yamlfile` (age, 1Password, Vault, SOPS)
- IDE integration via devpod's `--ide`
- Auto port detection
- NixOS module for packaged install
