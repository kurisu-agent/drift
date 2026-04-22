# drift — MVP development plan

Execution checklist derived from [`plans/archive/01-original-plan.md`](./plans/archive/01-original-plan.md). The original plan remains the contract/spec; this file is the ordered punch list to MVP.

**MVP definition.** A user can: install lakitu on a Linux circuit, install drift on a workstation, run `drift init` to register the circuit + a character, run `drift new <name> --clone <url>`, and `drift connect <name>` into a devcontainer over mosh. All commands in [CLI design](./plans/archive/01-original-plan.md#cli-design) work end-to-end. Everything in [Future](./plans/archive/01-original-plan.md#future) is explicitly out of scope.

Legend: `[x]` done · `[ ]` open · `[~]` partial.

When a phase goes fully `[x]`, move it to [plans/DONE.md](./plans/DONE.md) so this file only tracks open work.

All phases 0–17 are fully done — see [plans/DONE.md](./plans/DONE.md) for the historical record. No open MVP work remains in this file.

---

## Explicitly out of MVP scope

Tracked here for "no, that's later" answers. See [§ Future](./plans/archive/01-original-plan.md#future).

- Ports management (`drift ports`, conflict detection, per-workstation remap persistence)
- `lakitu serve` long-lived stdin/stdout RPC with batching/streaming notifications
- Cross-circuit sync of characters/tunes/chest
- Chest backends beyond `yamlfile` (age, 1Password, Vault, SOPS)
- IDE integration via devpod's `--ide`
- Auto port detection
- Log breadcrumbs on RPC errors — teach handlers to tee slog records into a request-scoped ring buffer and flush on error into `rpcerr.Error.Data["recent_logs"]`. `errfmt` + `slogfmt` already render them if present. See `plans/archive/04-nicer-logs.md` step 5 for the original design.
- `drift provision <host>` — one-shot circuit bootstrap over SSH. Default to the static-binary path: detect remote `uname -s -m`, pull matching `lakitu` + `devpod` assets from the latest drift release, install to `/usr/local/bin`, drop `packaging/systemd/lakitu-kart@.service` into `~/.config/systemd/user/`, `loginctl enable-linger`, run `lakitu init`. If `ssh host command -v nix` succeeds and flakes are enabled, prefer `nix profile install github:kurisu-agent/drift#lakitu`. Flags: `--no-nix` to force binary path, `--install-dir`, `--dry-run`.
- `drift migrate` cross-circuit — extend migrate beyond adopting a local devpod workspace into the current circuit. Move a kart from one registered circuit to another: snapshot the source kart (repo state, uncommitted changes, character binding, tune, chest refs), recreate it on the target circuit via `kart.new`, replay state, then optionally delete the source. Useful when a circuit is going away (attic box retired, VPS swap) or when latency shifts (Osaka ↔ London trip). Open questions: how to handle in-flight chest secrets that differ per-circuit, whether to stream the docker volume or re-clone from upstream, and whether to keep the source as a fallback until the user confirms the new kart works.
- Kart-config idempotency vs tune drift — edits to a tune's `devcontainer`, `mount_dirs`, `features`, or `dotfiles_repo` don't propagate to karts created before the edit. Resolver captures the tune at `kart.new` and persists the resolved shape on `garage/karts/<name>/config.yaml`; lifecycle ops (`kart.start` / `kart.restart`) replay that captured config, not the live tune. Safe on purpose (mounts and base image are container-creation-time anyway), but surprising when a user expects `drift tune set` to retroactively reshape existing karts. Options when we revisit: (a) a `drift kart sync <name>` verb that re-resolves from the current tune and recreates the container, (b) a passive drift-detection that flags stale karts on `drift list`, (c) opt-in `refresh_on_restart: true` per-kart. Pick one after we see which one users actually ask for.
- Zellij (or other multiplexer) auto-attach for `drift run` on the circuit — `drift connect <kart>` picks up Zellij because sshd hands off to an interactive login shell (which runs the circuit's `programs.zsh.interactiveShellInit` auto-attach block), but `drift run` passes a remote command, so zsh runs non-interactive and the init block is skipped. Fix without baking circuit-environment assumptions into the client: add an optional `wrap:` field on the registry entry (e.g. `wrap: zellij` → server renders `zellij attach -c <name> --force-run-commands -- sh -c '<cmd>'`). Circuits without Zellij leave it blank and nothing changes. Out of scope for the initial `drift run` polish — revisit when someone actually runs `drift run ai` on a Zellij-enabled box and wants the same UX as `drift connect`.
