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

When a phase goes fully `[x]`, move it to [plans/archive/DONE.md](./plans/archive/DONE.md)
so this file only tracks open work.

All phases 0–17 are fully done — see [plans/archive/DONE.md](./plans/archive/DONE.md)
for the historical record. No open MVP work remains in this file.

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
- `drift provision <host>` — one-shot circuit bootstrap over SSH. Default to the static-binary path: detect remote `uname -s -m`, pull matching `lakitu` + `devpod` assets from the latest drift release, install to `/usr/local/bin`, drop `packaging/systemd/lakitu-kart@.service` into `~/.config/systemd/user/`, `loginctl enable-linger`, run `lakitu init`. If `ssh host command -v nix` succeeds and flakes are enabled, prefer `nix profile install github:kurisu-agent/drift#lakitu`. Flags: `--no-nix` to force binary path, `--install-dir`, `--dry-run`.
