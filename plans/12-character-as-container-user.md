# character-as-container-user — normalise remoteUser in every kart

## Problem

devcontainer images ship with whatever non-root user the upstream image
author chose: `node` (typescript-node), `vscode` (universal), `ubuntu`
(base-ubuntu), `devuser` (some community features), sometimes no
non-root user at all. This leaks into lakitu in three places:

1. **Tune mounts can't target `$HOME` predictably.** The `default`
   tune on `dev-proxmox` currently bind-mounts
   `/home/dev/.claude → /home/vscode/.claude`. On the medscribe kart
   (typescript-node image, remoteUser is `node`) the mount lands at
   `/home/vscode/.claude` — a directory nothing in the container will
   ever read. The host dir shows up chowned to uid 1000 only because
   that uid happens to be `node` in that image; pure coincidence.
2. **Shell prompts and git-authored commits don't identify the
   character.** `node@medscribe` and `vscode@medscribe` tell you
   nothing about which character you're operating as; `kurisu@medscribe`
   tells you everything. Same kart, two characters, two karts on
   screen — currently indistinguishable from the prompt.
3. **Dotfiles / ssh-agent forwarding / gitconfig installation is
   image-specific.** Every feature that writes to `$HOME` has to guess
   what `$HOME` is, and lakitu's own postCreate steps (character
   gitconfig, dotfiles clone) carry image-specific branches or rely
   on `$HOME` expanding correctly at a specific lifecycle stage.

Plan 11 touches problem #1 from the config-surface angle
(`mount_dirs` as a first-class editable field), but does not solve the
underlying "which user does the container actually run as" question —
it just makes the wrong answer editable. This plan attacks the
substrate: **the kart's container user is always the kart's character,
by construction.**

## Goals

1. `remoteUser` in every kart's effective devcontainer.json is the
   character name. `kurisu@medscribe`, `akira@foo`, deterministic.
2. The character's home is `/home/<character>` in every kart
   regardless of image. Tune mounts can target `~/` / `$HOME` and
   land there. `.claude`, dotfiles, git config, ssh all live at a
   single well-known path.
3. Uid/gid continuity: the character's uid in-container matches
   whichever uid the image's default non-root user had (usually
   1000), so workspace file ownership — devpod clones the repo with
   that uid — stays clean. No chown sweeps.
4. Works across Debian/Ubuntu/Alpine-based devcontainer images with
   no image-specific branches in tunes. Images that ship without a
   non-root user get one created.
5. Character-name validation enforces POSIX username constraints at
   `character new` / `character set` time so this can't produce an
   invalid username downstream.

## Non-goals

- **Changing the character model beyond username validation.** The
  character key already is the username by convention; we formalise
  that and add validation, we don't split them.
- **Retroactive rewrite of existing karts.** The normalisation
  applies at kart-create time. Existing karts keep whatever user
  their image gave them until they're rebuilt. Rebuild-on-drift is
  plan 11's surface, not this one's.
- **Image-building.** We don't produce custom base images.
  Everything happens in `onCreateCommand` against whatever image the
  project's devcontainer.json names.
- **Windows / rootful-only images.** Out of scope; flag and fail
  clearly.

## Design

### Overlay shape

Extend the overlay devcontainer.json that `spliceMounts`
(`internal/kart/devcontainer.go:155-198`) writes to include two new
keys alongside the existing `mounts` splice:

```jsonc
{
  "remoteUser": "<character>",
  "onCreateCommand": "/usr/local/lakitu/normalize-user.sh <character> <uid-hint>",
  "mounts": [ /* existing splice */ ]
}
```

devpod merges this overlay against the project's devcontainer.json.
`remoteUser` in the overlay wins (devcontainer spec: last-wins for
scalar fields). `onCreateCommand` in the overlay is appended to any
project-side `onCreateCommand`, per devcontainer semantics.

### Normalisation script

`normalize-user.sh` is bind-mounted into the container at
`/usr/local/lakitu/normalize-user.sh` via an extra mount lakitu adds
to the overlay. Runs as root during `onCreateCommand` (before
remoteUser handoff).

Logic, in order:

1. **Resolve the default non-root user.** `getent passwd 1000`
   unless `LAKITU_EXPECTED_UID` overrides. Call that `$OLD`.
2. **Detect toolchain.** `command -v usermod` decides
   Debian/Ubuntu path vs. Alpine path. Alpine: `apk add --no-cache
   shadow` first, then fall through to the Debian path. (Cheaper
   than maintaining two code paths.)
3. **Branch by state:**
   - `$OLD` exists and equals `<character>`: no-op.
   - `$OLD` exists and differs: `usermod -l`, `groupmod -n`,
     `usermod -d /home/<character> -m`. Migrate dotfile references:
     `find /home/<character> -maxdepth 3 -type f \( -name '.*rc' -o
     -name '.profile' -o -name '.bash_profile' \) -exec sed -i
     "s|/home/$OLD|/home/<character>|g" {} +`. Rename
     `/etc/sudoers.d/$OLD` → `/etc/sudoers.d/<character>` with the
     same sed. Rename the user's primary group if it matched $OLD.
   - No uid-1000 user: `useradd -m -s /bin/bash -u 1000 -U
     <character>`, add to `sudo` / `wheel` group if present, drop a
     NOPASSWD sudoers file.
4. **Hand off.** `remoteUser` selects `<character>`; devpod's normal
   flow continues.

The script is idempotent — re-running on an already-normalised
container is a no-op. That matters because `onCreateCommand` runs
once per container, but rebuilds replay it.

### Where the logic lives in lakitu

- **Script**: ship `normalize-user.sh` in the repo at
  `internal/kart/normalize-user.sh`, embed with `//go:embed`.
  Materialise to a known path on the circuit at `lakitu init` (e.g.
  `~/.drift/bin/normalize-user.sh`). Overlay adds a `type=bind` mount
  from that path → `/usr/local/lakitu/normalize-user.sh`.
- **Overlay construction** extends `NormalizeDevcontainerWithMounts`
  (`internal/kart/devcontainer.go:104-147`):
  - Take the resolved character name as a new parameter
    (already available in `Resolver.Resolve` —
    `internal/kart/flags.go:133`).
  - After `spliceMounts`, call a sibling `spliceUserNormalisation`
    that sets `root["remoteUser"]`, prepends/concatenates the
    `onCreateCommand`, and adds the script bind-mount.
- **Plumb the character name** through `kart.New`
  (`internal/kart/new.go:132-134`) to the devcontainer call.
- **Character username validation** at `character new`
  (`internal/cli/lakitu/character_new.go`) and at plan-11's
  `character set name` path: regex `^[a-z][a-z0-9_-]{0,31}$`.
  Reject at the CLI edge with a clear error.

### Mount rewriting (the original trigger)

With this in place, the `default` tune's devcontainer.json can move
from a hardcoded `/home/vscode/.claude` target to a sentinel:

```jsonc
{ "mounts": ["source=${localEnv:HOME}/.claude,target=/home/${localEnv:LAKITU_CHARACTER}/.claude,type=bind,consistency=cached"] }
```

…or, cleaner, lakitu teaches `spliceMounts` to accept `~/` on the
**target** side of a mount and rewrite it to `/home/<character>/` at
splice time — symmetric with the existing `~/` → `${localEnv:HOME}/`
source rewrite at `flags.go:372-381`. Preferred: the latter, because
it stays a per-kart substitution and doesn't require devcontainer
variable-substitution that may not resolve in mount specs.

## Failure modes and open questions

1. **Image has baked-in absolute paths that aren't dotfiles** — e.g.
   a feature that dropped `/etc/profile.d/nvm.sh` with a literal
   `/home/node/.nvm` in it. `usermod -d -m` moves the directory but
   doesn't rewrite `/etc`. Mitigation: the sed sweep extends to
   `/etc/profile.d/*.sh`. Diminishing returns beyond that; accept
   some edge-case breakage and document it.
2. **Images that declare `containerUser: root`** (dev-on-root
   workflows). `remoteUser` override to a non-root user changes the
   behaviour users of those images expect. **Open:** opt-out per
   tune (`normalise_user: false`)? Opt-out per kart flag? Default-on
   everywhere and require explicit opt-out?
3. **Uid conflict with host bind mounts** — if a tune bind-mounts a
   host directory owned by a uid other than the character's
   in-container uid, files appear as `nobody`-owned. Not new;
   existing behaviour. Flag in docs.
4. **First-run cost** on Alpine images — `apk add shadow` on every
   create is a few seconds. Acceptable for the simplification it
   buys; revisit if users complain.
5. **Character rename after karts exist.** Plan 11 lets you rename
   a character; renaming the character should presumably rename the
   in-container user on every kart bound to it at next rebuild.
   Drift-detection side of plan 11 covers the prompt; this plan
   just needs the rebuild to do the right thing
   (idempotent script handles it).
6. **Root-only images** (no `useradd`, no shadow package
   available). Detect early, fail with a clear message:
   "image <X> does not support non-root users — set
   `normalise_user: false` on the tune to opt out."

## Open scoping questions (from conversation)

- **Fallback policy** when the image has no uid-1000 user: create
  fresh with uid 1000 + sudo (preferred), or fail loudly? Leaning
  create-fresh; flagging here because the failure-loud path is
  defensible if we want to force the user to notice.
- **Character key vs. username**: always identical? Or optional
  `username` override on the `Character` struct for cases where the
  key is namespaced (`kurisu-dotto-komu`) but the username should
  be short (`kurisu`)? Leaning identical-with-validation to keep
  the model flat; add the override later if it bites.
- **Opt-out granularity**: tune-level `normalise_user: false`,
  character-level, or both? Leaning tune-level only.

## Phases

1. **Script + embed.** Write `normalize-user.sh`. Unit-test the sed
   rewrites and branch logic in a tmpdir with a fake passwd file.
   Embed via `//go:embed`. Materialise at `lakitu init` to
   `~/.drift/bin/`.
2. **Overlay extension.** Add `spliceUserNormalisation` alongside
   `spliceMounts`. Thread character name through `kart.New` →
   `NormalizeDevcontainerWithMounts`. Overlay gains `remoteUser`,
   `onCreateCommand`, and the script bind-mount.
3. **Target-side `~/` rewrite** in `mergeMounts`
   (`flags.go:339-381`). Symmetric to the existing source-side
   rewrite; parameterised by character name.
4. **Username validation** at `character new` / `character set
   name`. Regex + `did you mean` for common typos.
5. **Tune opt-out** field (`normalise_user: bool`, defaulting true)
   in `model.Tune`; respected by the overlay builder.
6. **Update the `default` tune** on `dev-proxmox` to use `~/.claude`
   target. Delete and rebuild the medscribe kart. Verify
   `whoami` == `kurisu`, `ls ~/.claude` works, prompt shows
   `kurisu@medscribe`.

## Testing

- **Script unit tests** (shell, via `bats` or a Go-driven harness):
  rename in a tmpdir with a fabricated `/etc/passwd`, `/etc/group`,
  `/etc/sudoers.d/node`; assert final state. Idempotency run.
- **Integration** (Docker, in CI if available locally otherwise):
  matrix across `mcr.microsoft.com/devcontainers/typescript-node:22`,
  `mcr.microsoft.com/devcontainers/universal:2`,
  `mcr.microsoft.com/devcontainers/base:alpine`, and a minimal
  `debian:bookworm-slim` with no non-root user. For each: build a
  throwaway kart, exec `whoami && echo $HOME && id`, assert
  `whoami=<character>`, `$HOME=/home/<character>`, uid=1000.
- **Mount validation**: each of the above karts with a tune that
  mounts `source=$HOST/foo,target=~/foo`; exec `cat
  ~/foo/marker.txt` and assert content.
- **Opt-out**: tune with `normalise_user: false` — overlay omits
  `remoteUser` and `onCreateCommand`; kart runs as the image's
  original user.
- **Validation**: `character new BAD_NAME` rejected;
  `character new has spaces` rejected; `character new kurisu`
  accepted.
- **Manual smoke on dev-proxmox**: repro the current medscribe
  `/home/vscode/.claude` bug, apply this plan, rebuild, verify
  `/home/kurisu/.claude` contains the host's `.claude` contents and
  claude-code invocations inside the kart find their config.

## Out of scope / follow-ups

- **Custom base images** that pre-bake a `lakitu`-provided
  normalisation layer (skip the onCreate sed sweep). Worth it only
  if the sweep turns out to be flaky in the wild.
- **Per-kart character override** (`drift new foo --as akira`). Not
  needed now; karts already take a `--character` flag at create
  time, and that's what threads through.
- **Propagating character rename to running karts without
  rebuild.** Needs a `kart exec --as-root` primitive we don't have;
  defer.
- **Windows containers.** Separate universe.
- **Shell/dotfile provisioning** (prompt theming to display the
  character name distinctly, shared aliases per character). Plan-12
  unlocks this cleanly by giving every character a stable `$HOME`,
  but the provisioning itself is a separate follow-up.

## Relation to plan 11

Plan 11 makes `mount_dirs` editable without lossy writes and adds
drift detection on `drift connect`. This plan changes the *target*
semantics of those mount specs (`~/` on the target side) and changes
the effective container user. Ordering: 11 lands first (without it,
updating a tune to use the new `~/` target syntax would silently blow
away `env`). Then this plan lands on top, and the character-rename
drift-rebuild flow from 11 picks up the username change automatically
via the idempotent normalisation script.
