# Chest lifecycle env-var injection

## Problem

Chest secrets are only consumable through one narrow path today: a
character's `pat_secret: chest:<name>` is resolved at `kart.new` and baked
into Layer-1 dotfiles as `~/.config/gh/hosts.yml` + `~/.git-credentials`
(see `internal/kart/dotfiles.go:119,138`). Nothing else can reach chest
values, and nothing surfaces a chest value as an environment variable
anywhere in the kart's lifecycle.

Concrete failure motivating this: `devpod up` runs `install-dotfiles`
inside the container to clone the tune's `dotfiles_repo`. If that repo is
private (like `kurisu-dotto-komu/devpod-dotfiles`), the clone fails with
`403 Write access to repository not granted` because devpod's forwarded
credentials don't cover that repo. A `GITHUB_TOKEN` env var from chest,
present for that step, would resolve this — but there's no hook to
deliver one.

## Goals

1. Declare chest-backed env vars in the tune and have drift inject them at
   the correct lifecycle stage.
2. Identify every stage where a chest-resolved env var could matter, so
   future features (per-character env, per-kart overrides) plug into a
   known list instead of rediscovering stages.
3. Keep chest's existing invariant: values never appear on a flag or
   positional argument and never leave the circuit except into the kart
   they're scoped to.

## Non-goals

- New chest backends. YAML-file stays the only backend.
- Env-var injection from sources other than chest (plain literals,
  character fields, circuit config). Add those later if needed.
- Per-kart env overrides on the CLI (`drift new --env FOO=chest:bar`).
  Defer; the tune is the first and only source in this plan.
- Hot-reload of env vars on a running kart. Requires container restart.

## Lifecycle stages where env injection is meaningful

Labelled so later work can reference them by name. Stages run
top-to-bottom during `drift new`; the last two recur on subsequent
commands.

| # | Stage                                   | When                                                   | Who reads the env                                                                 | Mechanism                                                                                 |
|---|-----------------------------------------|--------------------------------------------------------|-----------------------------------------------------------------------------------|-------------------------------------------------------------------------------------------|
| 1 | **Dotfiles install (build one-shot)**  | `kart.new`, during `devpod up`'s install-dotfiles call | the tune `dotfiles_repo` clone (`git`) + the dotfiles install script it runs      | prepend env on the ssh-forwarded `install-dotfiles` command (`env KEY=VAL /usr/local/bin/devpod agent workspace install-dotfiles …`) — process-env only, never written anywhere |
| 2 | **devpod up `--set-env`**              | `kart.new`, during container provisioning              | every process in the container for its lifetime (container env, persists)        | `devpod up --set-env KEY=VALUE` (already mapped for ssh; add `UpOpts.SetEnv` symmetrically) |
| 3 | **kart start / restart**               | `kart.start`, `kart.restart`                           | re-hydrated container processes                                                  | same `--set-env` applied on re-up; keep the env source of truth in the kart config         |
| 4 | **drift connect / ssh**                | `kart.connect`, `kart.ssh`                             | the interactive shell and anything it launches                                   | existing `devpod.SSHOpts.SetEnv` / `SendEnv` (already defined, no current caller)          |

The four stages collapse to **three distinct injection sites** — stages
#2 and #3 both ride the same `containerEnv` set at `devpod up` time
(`start`/`restart` re-applies it on re-up). Stage #1 is one-shot:
process-env on the install-dotfiles invocation only, gone the moment
that process exits — secrets never land in the container's `containerEnv`
and never persist on disk. Stage #4 is session-scoped. The tune's `env`
structure below gives each injection site its own named block so the
user declares, per var, *where* it lands.

## Data model — tune

Add a nested `env` object to `model.Tune`, one key per injection site:

```go
// internal/model/types.go
type Tune struct {
    Starter      string  `yaml:"starter,omitempty"      json:"starter,omitempty"`
    Devcontainer string  `yaml:"devcontainer,omitempty" json:"devcontainer,omitempty"`
    DotfilesRepo string  `yaml:"dotfiles_repo,omitempty" json:"dotfiles_repo,omitempty"`
    Features     string  `yaml:"features,omitempty"     json:"features,omitempty"`
    Env          TuneEnv `yaml:"env,omitempty"          json:"env,omitempty"`
}

// TuneEnv groups chest-backed env vars by the injection site that
// consumes them. Every value must be a chest:<name> reference.
type TuneEnv struct {
    // Build is prepended as process-env on the in-container
    // `install-dotfiles` invocation during `devpod up`. Scoped to that
    // single process — the dotfiles install script + the git clone of
    // the tune's dotfiles_repo see it, nothing else. Never lands in the
    // container's containerEnv and is gone once provisioning completes;
    // `kart.restart` does NOT re-run install-dotfiles, so Build values
    // are genuinely one-shot.
    // Covers lifecycle stage #1. Fixes the dotfiles_repo 403 case.
    Build map[string]string `yaml:"build,omitempty" json:"build,omitempty"`

    // Workspace is passed to `devpod up --set-env` and becomes part of
    // the container's env for the workspace's lifetime — inherited by
    // every process, login or not, including background daemons and
    // re-applied on `kart.start` / `kart.restart`. Visible in
    // `docker inspect` for the workspace container.
    // Covers lifecycle stages #2 and #3.
    Workspace map[string]string `yaml:"workspace,omitempty" json:"workspace,omitempty"`

    // Session is passed to `devpod ssh --set-env` each time the user
    // opens a shell via `drift connect` / `drift ssh`. Scoped to the
    // ssh channel only — dies with the session, does not persist in
    // the container env, invisible to other processes in the kart.
    // Covers lifecycle stage #4.
    Session map[string]string `yaml:"session,omitempty" json:"session,omitempty"`
}
```

Every map value MUST start with `chest:`. Literal env values are rejected
at tune-write time for the same reason literal PATs are rejected on
characters (`internal/server/character.go:67`) — keeps secrets off disk
outside the chest. A key may appear in more than one block (e.g. same
name in `build` and `workspace`); each block is independent, with no
cross-block precedence.

Example (`~/.drift/garage/tunes/default.yaml`):

```yaml
dotfiles_repo: https://github.com/kurisu-dotto-komu/devpod-dotfiles
features: '{"ghcr.io/example-org/devpod-features/devtools:2":{}}'
env:
  build:
    # one-shot: visible to the tune's dotfiles_repo clone + install
    # script during `devpod up`, then gone. Perfect for auth tokens
    # you don't want persisting in the workspace env.
    GITHUB_TOKEN: chest:github-pat

  workspace:
    # present for every process in the container for its lifetime
    OPENAI_API_KEY: chest:openai

  session:
    # session-only; scoped to `drift connect` / `drift ssh` channels
    ANTHROPIC_API_KEY: chest:anthropic
```

## Plan

### Step 1 — extend the tune model + validator

- Add the `TuneEnv` struct and `Env TuneEnv` field on `model.Tune`.
- Teach `tune.add`/`tune.set` handlers to walk every value across all
  three blocks (`build`, `workspace`, `session`) and reject any whose
  prefix isn't `chest:`. Mirror the character handler's error
  (`rpcerr.TypeInvalidField`, message names the block and key).
- Assert stable render order across blocks in a tune-dump test
  (`yaml.v3` alphabetises maps, but the top-level block order is
  declaration-order from the struct tags).

### Step 2 — resolve chest references during `kart.new`

- In `internal/server/kart_new.go`, add `resolveTuneEnv(TuneEnv)
  (ResolvedTuneEnv, error)` alongside `resolvePATSecret`. Reuse the
  same `chest.Get` path; on miss, return `chest_entry_not_found` with
  both the offending block and key in `rpcerr.Data`.
- `ResolvedTuneEnv` mirrors `TuneEnv` but holds literal values — one
  `map[string]string` per injection site. Keeps stages independent
  downstream.
- Surface the resolved struct to `kart.New` via a new field on
  `kart.Resolved` next to Character, Tune, Features.
- No values leave the server handler until steps 3–6; keep them in
  memory only, never logged.

### Step 3 — thread `env.workspace` into `devpod up`

- Add `SetEnv []string` to `devpod.UpOpts` symmetric with `SSHOpts`, and
  map to `--set-env KEY=VALUE` in `args()`.
- At `internal/kart/new.go:150`, populate `up.SetEnv` from
  `resolved.Env.Workspace` (`KEY=VALUE`, stable order).
- Persist the resolved env **references** (keys + `chest:<name>` source,
  never literal values) for each block into the kart config so:
  (a) `kart.start`/`kart.restart` can re-resolve from chest on re-up
  without the user re-specifying, and
  (b) `kart info` can render what was loaded (see Step 8).
- Document on the tune validator and in the CLI help that
  `env.workspace` values are visible to every process in the container
  (via `/proc/<pid>/environ`, `printenv`, etc.) for the container's
  lifetime. Prefer `build` or `session` when a value doesn't need to
  be globally available.

### Step 4 — wrap `env.build` around the install-dotfiles call

- In `internal/kart/new.go`, when `resolved.Env.Build` is non-empty,
  prepend `env KEY=VALUE …` to the command string drift sends through
  `devpod.InstallDotfiles` (which today is `devpod agent workspace
  install-dotfiles --repository <file://…>`). The values live as
  process-env of the install-dotfiles process only; they never land in
  `containerEnv`, `/proc/1/environ`, or any persistent on-container
  state.

### Step 5 — re-apply workspace env on lifecycle verbs

- `kart.start` and `kart.restart` call `devpod up` under the hood —
  thread `resolved.Env.Workspace` through. Re-read chest on each
  invocation so rotated secrets land on restart.
- `env.build` is NOT re-applied on restart — install-dotfiles does not
  re-run. This is the defining property of the `build` bucket.
- `kart.delete` doesn't touch env; no change.

### Step 6 — thread `env.session` into `drift connect` / `drift ssh`

- At the connect call site (`internal/connect/connect.go`), resolve the
  tune via existing paths and populate `devpod.SSHOpts.SetEnv` from
  `resolved.Env.Session`. The `SendEnv`/`SetEnv` plumbing on
  `devpod.SSHOpts` already exists (`internal/devpod/devpod.go:189-190`)
  with no current caller — this is the first one.
- Per-invocation resolution: rotated chest values show up on the next
  `drift connect`.

### Step 7 — surface loaded env refs in `kart info`

- When a kart has any persisted env refs (from Step 3), `kart info`
  renders them grouped by block as `KEY: chest:<name>` per entry.
  Missing blocks are omitted so the output stays tight for karts with
  no env.
- Values are never rendered — only the chest reference (`chest:<name>`)
  and env key. Useful for debugging ("is this kart loading the right
  chest slot?") without exposing secrets.
- Client-side only: `drift info <kart>` shows the refs; nothing about
  loaded env is written into the container (no in-container `drift
  info` surface to scrape).

### Step 8 — integration tests

Mirror `integration/dotfiles_test.go` shape. One scenario per injection
site so regressions in one block can't mask the others:

- **build** — tune with a private `dotfiles_repo` + `env.build.GITHUB_TOKEN
  = chest:github-pat` where the chest value is a PAT with access to
  that repo. `kart.new` succeeds (proves the env reached the clone).
  Then `devpod ssh <name> --command 'printenv GITHUB_TOKEN'` returns
  empty (proves it did NOT leak into workspace env).
- **workspace** — `chest.set openai <v>`, tune
  `env.workspace.OPENAI_API_KEY = chest:openai`, `kart.new`,
  `devpod ssh <name> --command 'printenv OPENAI_API_KEY'` matches `<v>`.
  Then `kart.stop` + `kart.start` and assert the same printenv still
  returns the value (proves re-resolution on restart).
- **session** — tune `env.session.ANTHROPIC_API_KEY = chest:anthropic`,
  new kart, then `drift ssh <name> --command 'printenv
  ANTHROPIC_API_KEY'` matches; `devpod ssh` outside drift does NOT see
  it (proves session-only scope).
- **kart info rendering** — a kart with env entries in all three
  blocks surfaces them as `KEY: chest:<name>` per block in
  `drift info <kart>`; values never appear anywhere in the output.

Plus a negative test: unresolvable `chest:missing` in any block →
`kart.new` returns `chest_entry_not_found` with `block` and `key` in
the error data, and no container is left behind.

## Deferred decisions

- **Character env precedence.** A future character-level env map is
  out of scope here. When it lands, **character wins** on key
  collision with the tune — mirrors the existing character-wins
  behaviour for identity fields (`git_name`, `pat_secret`). Recorded
  so the later implementation doesn't have to re-litigate it.
