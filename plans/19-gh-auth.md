# gh-auth: per-kart runtime auth via gh CLI

## Problem

Plan 18 finished the PAT registry's *create-time* story: `drift new --pat=<slug>` (or auto-detect from `--clone`) wires a kart-level PAT, drift pre-clones server-side with that token, and the clone succeeds for private repos. But after that initial clone, every kart on the circuit shares the same runtime auth — devpod's `injectGitCredentials=true` brokers whatever's in the lakitu host's git credential store (testybox's `/etc/gitconfig` has `credential.helper = store` reading `~/.git-credentials`) into every container's credentials-server. So `kart.pat_slug` only scopes the *clone*; subsequent `git push`, `git pull`, and `gh` calls all use the host's catch-all token regardless of which character or PAT slug the kart was created with.

Two compounding artifacts confirm this end-to-end on testybox:

- A fresh test kart (with a non-host character) has `/home/node/.gitconfig` containing the lakitu host's `user.name`, not the character's.
- `gh auth status` inside the kart returns "not logged in," because devpod's credentials-server only configures git's credential helper, not gh's.

Layer-1 dotfiles was supposed to handle this — drift writes `~/.git-credentials` and `~/.config/gh/hosts.yml` during install. **It doesn't actually run.** Top-of-file comment in `internal/kart/new.go` (around the `[kart] installing layer-1 dotfiles` phase) already documents this:

> KNOWN LIMITATION (skevetter/devpod v0.22): install-dotfiles runs inside the agent context; a file:// URL written to the host tmpdir isn't visible there, so the git-clone silently pulls an empty repo or errors quietly. Command returns success but layer-1 files do not land in the container. Planned follow-up: post-up `devpod ssh --command` with the script piped over stdin.

So the layer-1 dotfiles flow is and has been a no-op. Anything we put in `install.sh` never executes.

## Goals

1. **Per-kart runtime auth.** `git push` / `gh pr create` from inside a kart use the PAT bound to that kart's character (or kart pat slug), not whatever's in the lakitu host's credential store. Two karts with different characters on the same circuit can authenticate as different GitHub identities at runtime.
2. **gh CLI usable.** `gh` is on `$PATH` and pre-authenticated, so Claude Code (and humans) can run `gh pr create`, `gh workflow run`, `gh api`, etc. without any login dance. `git push/pull` go through gh's credential helper transparently.
3. **No host bleed.** Disable devpod's `injectGitCredentials` so the kart's git config and credential helper are entirely drift's to set, not a snapshot of testybox's `/etc/gitconfig`.
4. **Conditional CLAUDE.md advisory.** Seed CLAUDE.md tells Claude Code to use gh for git/API work — but only on karts where drift actually wired gh auth (no PAT → no advice, so the stamp doesn't promise auth that doesn't exist).

## Non-goals

- Fixing devpod's install-dotfiles bug upstream. We work around it.
- Replacing the chest as the secret store. PAT material still flows from `pats/<slug>.yaml → chest_ref → chest dechest`.
- Multi-account in a single kart. One PAT in, one identity out.

## What's already correct in `feat/gh-auth` (uncommitted)

The branch at `.claude/worktrees/gh-auth` (off `main` at `6d299d4`) has working pieces that survive the rewrite below:

- `internal/devpod/devpod.go` — `Client.ensureContextOptions(ctx)` runs `devpod context set-options -o SSH_INJECT_GIT_CREDENTIALS=false` once per Client lifetime before the first `Up`. **Keep.** This stops the credential.helper injection. (Caveat: it does *not* stop the `[user]` block injection; see fix below.)
- `internal/kart/flags.go` — `injectGithubCLIFeature` adds `ghcr.io/devcontainers/features/github-cli:1` to the kart's features when the resolved character has a PAT. **Keep.** Mirrors `injectNixosOrgFeature`. The redundancy with nixenv-tune-bundled `gh` is intentional belt-and-braces (devcontainer feature lands `gh` at `/usr/local/bin/gh` during image build, available before any postCreate runs).
- `internal/seed/builtins.go` — `claudeGHAuthBlock` constant, gated by `{{ if .HasPAT }}`, appended to `claudeCodeMD`. **Keep.**
- `internal/kart/seed_fragment.go` — `kartVars` populates `HasPAT = "true"` when `r.Character != nil && r.Character.PAT != ""`. **Keep.**
- `internal/seed/seed_test.go` — `TestClaudeCodeMD_GHAuthBlock_ConditionalOnHasPAT`. **Keep.**

## What needs rework

The current branch wired the gh-auth flow into `WriteLayer1Dotfiles` + `writeInstallScript` + `DotfilesScriptEnv` on the install-dotfiles call. That whole delivery channel is broken (see Problem). It has to move.

The right channel is the post-up `devpod ssh --command` script that already runs in `kart.New` for symlinks / mount copies / seeds / ssh login alias. That fragment runs inside the container shell with stdin → bash, after `devpod up` returns and before drift hands the user `drift connect`. It works. It's the same path the SSH alias logic already uses successfully.

Concretely:

- Drop `WriteLayer1Dotfiles`'s `writeGhHosts` / `writeGitCredentials` / `writeGitConfig` and `writeInstallScript` entirely. Layer-1 dotfiles is dead code on this devpod fork; admit it and remove it. The `initEphemeralGitRepo` + `install-dotfiles` call in `kart.New` go away too.
- Drop `kart.New`'s `InstallDotfilesWithOpts` invocation (around line 286 of `internal/kart/new.go`).
- Drop `layer1CharacterEnv` (the `DRIFT_GIT_*` env-var helper added in this branch). It's correct but plumbed into the wrong call.
- Add a **character-setup script fragment** that runs in the existing post-up `script.Run(ctx, d.Devpod, resolved.Name)` block. The fragment needs:
  - `DRIFT_GITHUB_PAT`, `DRIFT_GIT_NAME`, `DRIFT_GIT_EMAIL`, `DRIFT_GITHUB_USER` available as env. Devpod's `ssh --set-env KEY=VALUE` flag exists; use it. Or pipe `KEY=VALUE; ...` lines as the first chunk of stdin and let the script `eval` them. (Check `internal/kart/container_script.go` and `devpod.SSHOpts.SetEnv` — there's a `--set-env` plumbing for the session-env path that this can ride on.)
  - The script body:
    ```sh
    if [ -n "${DRIFT_GIT_NAME:-}" ];  then git config --global user.name  "$DRIFT_GIT_NAME";  fi
    if [ -n "${DRIFT_GIT_EMAIL:-}" ]; then git config --global user.email "$DRIFT_GIT_EMAIL"; fi
    if [ -n "${DRIFT_GITHUB_PAT:-}" ] && command -v gh >/dev/null 2>&1; then
        printf '%s\n' "$DRIFT_GITHUB_PAT" | gh auth login --with-token --hostname github.com
        gh auth setup-git --hostname github.com
    fi
    ```
  - These `git config --global` calls will *override* devpod's `--configure-git-helper`-written `[user]` block since they run after `devpod up` finishes. Solves the "kart got the host's user.name" bug without needing a separate devpod opt-out.

- The github-cli devcontainer feature has to land BEFORE the script fragment runs. It does — feature install happens during image build, the script runs after `devpod up`. Confirm in tests / live verification.

## Open questions

- **`devpod ssh --set-env` with secret values.** Does `--set-env` expose the value on argv (visible in `ps`)? If yes, switch to stdin-piped `export DRIFT_GITHUB_PAT=...; ...` instead. `internal/exec.RedactSecrets` already covers `github_pat_*` literals in argv echo, but argv is only redacted in *drift's* logs — the kernel-level argv table is still readable by anyone in the container's pid namespace. Verify before shipping.
- **gh `setup-git` interaction with non-https remotes.** If a kart's clone source was an SSH URL (`git@github.com:foo/bar`), `gh auth setup-git` configures only HTTPS. Acceptable for v1 — drift's pre-clone path only fires for github HTTPS — but worth a comment.
- **Token rotation.** The kart's gh login is captured at create time. Rotating the chest entry behind the slug doesn't refresh in-kart auth until kart recreate. Plan 18's `drift kart relink-pat` (deferred) would handle this. Out of scope here; mention in CLAUDE.md.
- **Multi-host gh auth.** `gh auth login --hostname github.com` is enough for v1. GHE comes later via tune config or character extension.

## Test plan

- Unit: extend `kart_test.go` to assert the post-up script fragment for a character-with-PAT kart contains the gh-auth + git-config commands, and is *absent* (or just the SSH-alias bits) for a no-PAT kart.
- Live on testybox: recreate the test kart, verify
  - `gh auth status` reports authenticated as the character's identity.
  - `git config user.name` matches the character, not testybox's host identity.
  - `git push` works without a `~/.git-credentials` file.
  - `~/.claude/CLAUDE.md` contains the gh-auth advisory.
  - A second kart with a different character authenticates as that character (run-cross-kart test).
- Integration: probably out of scope for this slice; integration tests use a stub devpod that doesn't exercise the credentials-server.

## Branch handoff

`.claude/worktrees/gh-auth` is sitting with the wrong delivery channel wired up. Cleanest path for the next session:

1. Reset the worktree's `internal/kart/dotfiles.go`, `internal/kart/new.go`, `internal/kart/dotfiles_test.go` back to `origin/main` (`git checkout origin/main -- <files>`).
2. Keep the survivors listed in "What's already correct" above.
3. Implement the post-up script fragment per the sketch.
4. `make ci` + redeploy via `--override-input` + recreate a test kart for live verification.
5. Push and open PR.

If reset-and-rebuild feels heavier than starting clean, just delete the worktree (`git worktree remove .claude/worktrees/gh-auth`) and start a new branch off main; the survivors are small enough to retype.
