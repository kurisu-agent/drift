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

Phases 0–13, 16, 17 are fully done — see [archive/DONE.md](./archive/DONE.md)
for the historical record. Only phases with open or partial items are tracked below.

---

## Phase 14 — Human CLI error formatting

- [x] stderr format ([PLAN.md § stderr format](./PLAN.md#stderr-format-human-cli-path)): line 1 `error: <message>`, line 2 single-line JSON of the error object; exit code mirrors `Code` — implemented by `internal/cli/errfmt.Emit`; drift + lakitu CLIs refactored to route through it
- [~] Idempotency contract verified per verb ([PLAN.md § Idempotency](./PLAN.md#idempotency)) — start/stop/restart/delete covered by `internal/server/kart_lifecycle_test.go`. Enable/disable still uncovered at the handler level: `internal/systemd/systemd_test.go` asserts argv construction only, and `internal/server/kart_autostart.go` has no test asserting a double-enable/double-disable is a no-op. `systemctl --user enable|disable --now` is idempotent from systemd's side and the marker write/remove is idempotent by construction, but nothing pins that contract.
- [x] Unit tests assert the two-line format + exit code for every error code category (2 user, 3 not_found, 4 conflict, 5 devpod, 6 auth) in `internal/cli/errfmt/errfmt_test.go`; testscript-level golden tests deferred — unit coverage is stricter and easier to maintain

---

## Phase 15 — Integration harness (tier-2 tests)

- [x] Dockerfile for a "circuit" image at `integration/Dockerfile.circuit`: Debian-slim + sshd + devpod + lakitu (docker access is delegated to the devcontainer's outer daemon via socket bind, matching plans/PLAN.md § "Integration harness")
- [x] Test driver at `integration/harness.go`: builds the image, spins a per-test container on an ephemeral port, generates an ed25519 keypair, writes a per-test ssh config, and exposes `Circuit.Drift(ctx, args...)` so tests drive the real `drift` binary over real SSH
- [x] Build-tag-gated (`//go:build integration`) so unit `go test ./...` stays fast
- [~] Cover: warmup probe, kart.new with `--clone`, connect via ssh fallback, kart.delete, character add+list, chest set+get. Done in `integration/`: init+version (`TestDriftInitAndServerVersion`), probe via circuit add (`TestCircuitAddProbe`), character add/list/show/remove (`TestCharacterLifecycle`), chest set/get/list/rm incl. multiline values (`TestChestLifecycle`), end-to-end ssh-proxy (`TestSSHProxyEchoOK`), tune/features/dotfiles coverage (`TestTuneStarter`, `TestTuneDevcontainer`, `TestTuneDotfilesRepo`, `TestTuneFeatures`, `TestFeaturesFlagExplicit`, `TestFeaturesAdditiveMerge`, `TestLayer1Dotfilesland`), AI command (`TestAICommand`), and full `drift new` + `drift delete` against real devpod with the host daemon bind-mounted (`TestRealDevpodUpAndDelete` in `realdevpod_test.go`). Remaining gaps: `kart.new --clone` variant has no integration test (only `--starter` does), and `drift connect` has no integration coverage.
- [x] CI job target in `Makefile`: `make integration`

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
