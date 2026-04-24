# drift — TODOs

- Ports management (`drift ports`, conflict detection, per-workstation remap persistence).
- `lakitu serve` long-lived stdin/stdout RPC with batching/streaming notifications.
- Cross-circuit sync of characters/tunes/chest.
- Chest backends beyond `yamlfile` (age, 1Password, Vault, SOPS).
- IDE integration via devpod's `--ide`.
- Auto port detection.
- Log breadcrumbs on RPC errors — surface recent per-request log lines in the error payload so clients see context on failure. See `plans/archive/04-nicer-logs.md` step 5.
- `drift provision <host>` — one-shot circuit bootstrap over SSH that installs `lakitu` + `devpod`, wires up the systemd user template, and runs `lakitu init`.
- `drift migrate` cross-circuit — designed in [plans/09-migrate-cross-circuit.md](plans/09-migrate-cross-circuit.md). Move a kart's config from one circuit to another and recreate it there.
- Kart-config idempotency vs tune drift — edits to a tune don't propagate to karts created before the edit. Surface the staleness (or offer a refresh verb) when users expect retroactive reshaping.
- Zellij auto-attach for `drift run` — `drift connect` picks up Zellij via the interactive login shell, but `drift run` passes a remote command and skips it. Add a server-side wrap so `drift run` gets the same session UX.
- Zellij as a first-class feature — ship it alongside lakitu/devpod with an opinionated layout and have connect/run attach by default.
- Auto-mount `~/.claude` into karts — when the workstation has `~/.claude/` populated, `drift new` should splice a bind mount automatically so AI-skill workflows just work.
- Sidecar SSH tunnel for mosh port forwards — mosh can't carry TCP forwards, so devpod's forwards collapse under mosh. Hold a parallel `ssh -N -L` for each `forwardPorts` entry alongside the mosh session.
- Port-forward opt-in for mosh (interim) — until the sidecar lands, prompt or require a flag before attempting forwards on mosh so users don't see spurious `use of closed network connection` errors.
- drift features — bundle the devcontainer-feature blobs we keep hand-passing into `tune --features` (claude-code, zellij, etc.) into named presets, so a tune can declare `features: [ours/claude, ours/zellij]` instead of pasting JSON.
