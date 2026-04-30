# drift — TODOs

## Next

- user-test latest PR features

## Later

- `drift ports` bubbletea TUI — browse/edit all forwards across karts, with auto-detect via `ss -tlnp` over the existing master. Layout + behaviour is fully spec'd in `plans/13-drift-ports-sidecar.md` §TUI; just needs implementing in a follow-up PR.
- `lakitu serve` long-lived stdin/stdout RPC with batching/streaming notifications.
- Cross-circuit sync of characters/tunes/chest.
- Chest backends beyond `yamlfile` (age, 1Password, Vault, SOPS).
- IDE integration via devpod's `--ide`.
- Log breadcrumbs on RPC errors — surface recent per-request log lines in the error payload so clients see context on failure. See `plans/archive/04-nicer-logs.md` step 5.
- `drift provision <host>` — one-shot circuit bootstrap over SSH that installs `lakitu` + `devpod`, wires up the systemd user template, and runs `lakitu init`.
- `drift migrate` cross-circuit — designed in [plans/09-migrate-cross-circuit.md](plans/09-migrate-cross-circuit.md). Move a kart's config from one circuit to another and recreate it there.
- Kart-config idempotency vs tune drift — edits to a tune don't propagate to karts created before the edit. Surface the staleness (or offer a refresh verb) when users expect retroactive reshaping.
- Flake drift on connect — if the kart's tune `flake_uri` resolves to a newer commit than the one installed in the kart's nix profile, `drift connect` should prompt to refresh (mirroring the tune-drift recreate prompt). Today the only way to pick up flake updates is `nix profile upgrade` inside the kart or recreating it.
- Stale kart cleanup — failed `kart.new` runs and abandoned test karts accumulate in docker (containers + per-workspace images at 1-2 GB each) and exhaust circuit disk; add a `lakitu kart prune` for orphans / age-based GC.
- Zellij auto-attach for `drift run` — `drift connect` picks up Zellij via the interactive login shell, but `drift run` passes a remote command and skips it. Add a server-side wrap so `drift run` gets the same session UX.
- Default login shell to zsh in nix-feature karts — flake ships zsh, but the login shell stays whatever the base image set. `driftShell` seed exec's zsh from `.bashrc` today; a real `chsh` would be cleaner.
- Consider devenv.sh for richer presets — natural fit for per-language `drift-{go,rust,python}` toolkits; either as an alternate container type or as the base of richer presets.
- yazi config + piper plugin in `nixenv` — flake installs the binary, but `yazi.toml` + piper plugin install have no home yet.
- Auto-mount `~/.claude` into karts — when the workstation has `~/.claude/` populated, `drift new` should splice a bind mount automatically so AI-skill workflows just work.
- drift features — bundle the devcontainer-feature blobs we keep hand-passing into `tune --features` (claude-code, zellij, etc.) into named presets, so a tune can declare `features: [ours/claude, ours/zellij]` instead of pasting JSON.
- Starter karts land with an empty `/workspaces/<name>/` — repro: `lakitu kart new <n> --tune default --starter https://github.com/kurisu-agent/drift`, kart reaches `running`, but inside the workspace container the project dir is empty (`ls /workspaces/<n>` shows zero entries, link count 0). `drift connect` lands the user in a cwd that vanishes, zellij panics on `Unable to read current working directory`. Clone karts are unaffected — the starter-source staging step (clone → strip history → re-init) seems to land somewhere devpod isn't picking up. Investigate whether the staging tmpdir is reachable from the agent context at devpod-up time, and whether the starter source argument is being passed through correctly under recent devpod fork builds.
- Split cliscript out of `test-race` — `go test -race ./...` race-instruments the spawned `drift`/`lakitu` binaries that cliscript's testscript suite runs as subprocesses, blowing `make ci` from ~1 min to ~10 min. Race coverage on cliscript catches little (testscript is sequential; real concurrency lives in `internal/server`, `internal/kart`, `internal/rpc`). Move to a separate `test-cliscript` target (no `-race`) wired into `make ci` alongside the now-narrower `test-race`.
- Pangolin integration — expose kart-side services through a self-hosted Pangolin tunnel so workstations (and humans) can reach them without bespoke `ssh -L` forwards or circuit-side ingress config.
- `drift chest` workstation-side CLI — today chest operations only exist on `lakitu chest {list,get,set,new,rm}` and require an SSH hop to the circuit. Mirror them on the `drift` client (`drift chest list`, `drift chest set <name>` reading from stdin, etc.) so secret rotation lives in the same surface as `drift kart` / `drift tune`.
- Run `/simplify` across the whole repo.
- nginx NAR cache invalidation on `nix store gc` — the harmonia front-cache can serve a path nginx still has cached but harmonia would now 404 on after a store gc. Worst case is bit-identical (paths are content-addressed) but a stale 404-vs-200 mismatch is possible; clear `/var/cache/nginx/harmonia/` after a manual gc until we wire an automatic purge.
- Document + standardise credential injection points. Today a character's `pat_secret` ends up in several places via different paths and different timing: the layer-1 dotfiles write `~/.git-credentials` inside the container for post-clone commits; `tune.env.build.GITHUB_TOKEN` piggybacks on `--dotfiles-script-env` for the install script; and the initial `devpod up` clone relies entirely on the circuit's host-side `~/.git-credentials` plus devpod's `injectGitCredentials`. These are three separate hops that look interchangeable from the outside but aren't. Spec out (a) the full set of sites secrets can land, (b) which chest key feeds each, (c) one convention (likely: inject character PAT into github HTTPS `SourceURL` at resolve time) so `drift new <priv-repo>` works without manual `~/.git-credentials` seeding on the circuit.
- Replace per-kart PATs with a circuit-side gh proxy — wrap `gh` in the kart so all GitHub traffic flows through an HTTP-authed service on lakitu that holds the real token, mediates calls against deny/approve filters, and audits per-kart usage. Removes raw PATs from kart filesystems and gives one place to rotate, revoke, and scope.
