# Chest lifecycle env-var injection

Status: proposed
Owner: unassigned
Related: `internal/chest/`, `internal/server/chest.go`, `internal/server/kart_new.go`, `internal/kart/new.go`, `internal/kart/dotfiles.go`, `internal/kart/flags.go`, `internal/model/types.go`, `internal/devpod/devpod.go`

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

| # | Stage                          | When                                                   | Who reads the env                                                                 | Mechanism                                                                                 |
|---|--------------------------------|--------------------------------------------------------|-----------------------------------------------------------------------------------|-------------------------------------------------------------------------------------------|
| 1 | **Layer-1 dotfiles build**    | `kart.new`, drift-side on the host                    | drift's own dotfiles writer + any script sourced from the character layer        | write to a sourced file (e.g. `~/.config/drift/env.sh`) materialised in the Layer-1 tmpdir |
| 2 | **devpod up `--set-env`**     | `kart.new`, during container provisioning              | every process in the container for its lifetime (container env, persists)        | `devpod up --set-env KEY=VALUE` (already mapped for ssh; add `UpOpts.SetEnv` symmetrically) |
| 3 | **Layer-2 dotfiles install**  | `devpod up` → `install-dotfiles` inside the container  | the tune `dotfiles_repo` clone + the dotfiles install script it runs             | container env set via stage #2 covers this — `git` picks up `GITHUB_TOKEN` automatically   |
| 4 | **kart start / restart**      | `kart.start`, `kart.restart`                           | re-hydrated container processes                                                  | same `--set-env` applied on re-up; keep the env source of truth in the kart config         |
| 5 | **drift connect / ssh**       | `kart.connect`, `kart.ssh`                             | the interactive shell and anything it launches                                   | existing `devpod.SSHOpts.SetEnv` / `SendEnv` (already defined, no current caller)          |

The five stages collapse to **three distinct injection sites** — stages
#2/#3/#4 all ride the same `containerEnv` set at `devpod up` time (the
in-container `install-dotfiles` inherits it; `start`/`restart` re-applies
it on re-up). Stage #1 is host-side at Layer-1 write time; stage #5 is
session-scoped. The tune's `env` structure below gives each injection
site its own named block so the user declares, per var, *where* it lands.

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
    // Layer1 is written into the Layer-1 dotfiles tmpdir on the host as a
    // sourced file (e.g. ~/.config/drift/env.sh) — available to the
    // character's dotfiles install script and the user's shell via rc-file.
    // Covers lifecycle stage #1.
    Layer1 map[string]string `yaml:"layer1,omitempty" json:"layer1,omitempty"`

    // Container is passed to `devpod up --set-env` and becomes part of the
    // container's env for its lifetime. Inherited by the in-container
    // `install-dotfiles` clone (fixes the tune dotfiles 403 case) and
    // re-applied on `kart.start` / `kart.restart`.
    // Covers lifecycle stages #2, #3, and #4.
    Container map[string]string `yaml:"container,omitempty" json:"container,omitempty"`

    // Connect is passed to `devpod ssh --set-env` each time the user opens
    // a session via `drift connect` / `drift ssh`. Does not persist in the
    // container env — scoped to the ssh channel only.
    // Covers lifecycle stage #5.
    Connect map[string]string `yaml:"connect,omitempty" json:"connect,omitempty"`
}
```

Every map value MUST start with `chest:`. Literal env values are rejected
at tune-write time for the same reason literal PATs are rejected on
characters (`internal/server/character.go:67`) — keeps secrets off disk
outside the chest. A key may appear in more than one block (e.g. same
name for Layer-1 and Container); each block is independent, with no
cross-block precedence.

Example (`~/.drift/garage/tunes/default.yaml`):

```yaml
dotfiles_repo: https://github.com/kurisu-dotto-komu/devpod-dotfiles
features: '{"ghcr.io/example-org/devpod-features/devtools:2":{}}'
env:
  layer1:
    # sourced by the character dotfiles install script
    GIT_AUTHOR_EMAIL: chest:git-author-email

  container:
    # present for every process in the kart; git picks this up during the
    # tune's dotfiles_repo clone inside `devpod up install-dotfiles`
    GITHUB_TOKEN: chest:github-pat
    OPENAI_API_KEY: chest:openai

  connect:
    # session-only; handy for one-off CLI tools the user runs interactively
    ANTHROPIC_API_KEY: chest:anthropic
```

## Plan

### Step 1 — extend the tune model + validator

- Add the `TuneEnv` struct and `Env TuneEnv` field on `model.Tune`.
- Teach `tune.add`/`tune.set` handlers to walk every value across all
  three blocks (`layer1`, `container`, `connect`) and reject any whose
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
- No values leave the server handler until steps 3–5; keep them in
  memory only, never logged.

### Step 3 — thread `env.container` into `devpod up`

- Add `SetEnv []string` to `devpod.UpOpts` symmetric with `SSHOpts`, and
  map to `--set-env KEY=VALUE` in `args()`.
- At `internal/kart/new.go:150`, populate `up.SetEnv` from
  `resolved.Env.Container` (`KEY=VALUE`, stable order).
- Persist the set of container-env keys (NOT values) into the kart
  config so `kart.start`/`kart.restart` can re-resolve them from chest
  on re-up without the user re-specifying.

### Step 4 — write `env.layer1` into the Layer-1 dotfiles tmpdir

- In `internal/kart/dotfiles.go` (`WriteLayer1Dotfiles`), when
  `resolved.Env.Layer1` is non-empty, emit `~/.config/drift/env.sh` with
  `export KEY="VALUE"` lines (values shell-quoted) into the Layer-1
  tmpdir and have the layer-1 install script source it.
- File mode 0600 inside the tmpdir; devpod's `install-dotfiles` copies
  it into the container with the same mode.

### Step 5 — re-apply container env on lifecycle verbs

- `kart.start` and `kart.restart` call `devpod up` under the hood —
  thread `resolved.Env.Container` through. Re-read chest on each
  invocation so rotated secrets land on restart.
- `kart.delete` doesn't touch env; no change.

### Step 6 — thread `env.connect` into `drift connect` / `drift ssh`

- At the connect call site (`internal/connect/connect.go`), resolve the
  tune via existing paths and populate `devpod.SSHOpts.SetEnv` from
  `resolved.Env.Connect`. The `SendEnv`/`SetEnv` plumbing on
  `devpod.SSHOpts` already exists (`internal/devpod/devpod.go:189-190`)
  with no current caller — this is the first one.
- Per-invocation resolution: rotated chest values show up on the next
  `drift connect`.

### Step 7 — integration tests

Mirror `integration/dotfiles_test.go` shape. One scenario per injection
site so regressions in one block can't mask the others:

- **container** — `chest.set github-pat <v>`, tune
  `env.container.GITHUB_TOKEN = chest:github-pat`, `kart.new`,
  `devpod ssh <name> --command 'printenv GITHUB_TOKEN'` matches `<v>`.
- **layer1** — tune `env.layer1.FOO = chest:foo`, new kart, shell
  into the container, assert `FOO` is set in a login shell (sourced
  from `~/.config/drift/env.sh` via rc) and NOT in a non-login
  `docker exec` (layer-1 is rc-scoped, not containerEnv).
- **connect** — tune `env.connect.BAR = chest:bar`, new kart, then
  `drift ssh <name> --command 'printenv BAR'` matches; `devpod ssh`
  outside drift does NOT see it (proves the scope).

Plus a negative test: unresolvable `chest:missing` in any block →
`kart.new` returns `chest_entry_not_found` with `block` and `key` in
the error data, and no container is left behind.

## Open questions

- **Leak surface.** `devpod up --set-env KEY=VALUE` puts the value on
  argv of the host-side `devpod` process. `ps` on the circuit would see
  it while up is running. Alternatives: write a tmpfile and use
  `--env-file` if devpod supports it, or ship an extra-devcontainer-path
  that declares `containerEnv` and never crosses argv. Decide before
  implementation.
- **Precedence.** If a future character-level env map overlaps a tune
  env map, character wins or tune wins? Defer but record the choice.
- **Status rendering.** Should `drift kart info` list the env keys (not
  values) the kart was booted with? Useful for debugging, cheap to add.
