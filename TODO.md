# drift — TODOs

- `drift ports` bubbletea TUI — browse/edit all forwards across karts, with auto-detect via `ss -tlnp` over the existing master. Layout + behaviour is fully spec'd in `plans/13-drift-ports-sidecar.md` §TUI; just needs implementing in a follow-up PR.
- `lakitu serve` long-lived stdin/stdout RPC with batching/streaming notifications.
- Cross-circuit sync of characters/tunes/chest.
- Chest backends beyond `yamlfile` (age, 1Password, Vault, SOPS).
- IDE integration via devpod's `--ide`.
- Log breadcrumbs on RPC errors — surface recent per-request log lines in the error payload so clients see context on failure. See `plans/archive/04-nicer-logs.md` step 5.
- `drift provision <host>` — one-shot circuit bootstrap over SSH that installs `lakitu` + `devpod`, wires up the systemd user template, and runs `lakitu init`.
- `drift migrate` cross-circuit — designed in [plans/09-migrate-cross-circuit.md](plans/09-migrate-cross-circuit.md). Move a kart's config from one circuit to another and recreate it there.
- Kart-config idempotency vs tune drift — edits to a tune don't propagate to karts created before the edit. Surface the staleness (or offer a refresh verb) when users expect retroactive reshaping.
- Zellij auto-attach for `drift run` — `drift connect` picks up Zellij via the interactive login shell, but `drift run` passes a remote command and skips it. Add a server-side wrap so `drift run` gets the same session UX.
- Zellij as a first-class feature — ship it alongside lakitu/devpod with an opinionated layout and have connect/run attach by default.
- Auto-mount `~/.claude` into karts — when the workstation has `~/.claude/` populated, `drift new` should splice a bind mount automatically so AI-skill workflows just work.
- drift features — bundle the devcontainer-feature blobs we keep hand-passing into `tune --features` (claude-code, zellij, etc.) into named presets, so a tune can declare `features: [ours/claude, ours/zellij]` instead of pasting JSON.
- Document + standardise credential injection points. Today a character's `pat_secret` ends up in several places via different paths and different timing: the layer-1 dotfiles write `~/.git-credentials` inside the container for post-clone commits; `tune.env.build.GITHUB_TOKEN` piggybacks on `--dotfiles-script-env` for the install script; and the initial `devpod up` clone relies entirely on the circuit's host-side `~/.git-credentials` plus devpod's `injectGitCredentials`. These are three separate hops that look interchangeable from the outside but aren't. Spec out (a) the full set of sites secrets can land, (b) which chest key feeds each, (c) one convention (likely: inject character PAT into github HTTPS `SourceURL` at resolve time) so `drift new <priv-repo>` works without manual `~/.git-credentials` seeding on the circuit.
