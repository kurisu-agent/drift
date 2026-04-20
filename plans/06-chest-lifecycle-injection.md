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

Stages #2 and #3 share one mechanism: `containerEnv` set at `devpod up`
time is inherited by every child process, including the in-container
`install-dotfiles` invocation, so a single injection covers both. Stage
#5 is session-scoped and orthogonal.

## Data model — tune

Add an `env` map to `model.Tune`:

```go
// internal/model/types.go
type Tune struct {
    Starter      string            `yaml:"starter,omitempty" json:"starter,omitempty"`
    Devcontainer string            `yaml:"devcontainer,omitempty" json:"devcontainer,omitempty"`
    DotfilesRepo string            `yaml:"dotfiles_repo,omitempty" json:"dotfiles_repo,omitempty"`
    Features     string            `yaml:"features,omitempty" json:"features,omitempty"`
    Env          map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
}
```

`Env` values follow the existing chest reference shape: every value MUST
start with `chest:`. Literal env values are rejected at tune-write time
for the same reason literal PATs are rejected on characters
(`internal/server/character.go:67`) — keeps secrets off disk outside the
chest.

Example (`~/.drift/garage/tunes/default.yaml`):

```yaml
dotfiles_repo: https://github.com/kurisu-dotto-komu/devpod-dotfiles
features: '{"ghcr.io/example-org/devpod-features/devtools:2":{}}'
env:
  GITHUB_TOKEN: chest:github-pat
  OPENAI_API_KEY: chest:openai
```

## Plan

### Step 1 — extend the tune model + validator

- Add `Env map[string]string` to `model.Tune`.
- Teach `tune.add`/`tune.set` handlers (wherever they live) to reject any
  value whose prefix isn't `chest:`. Mirror the character handler's
  error (`rpcerr.TypeInvalidField`, message names the field).
- Update any tune-dump path so `env` renders in stable order (YAML maps
  already alphabetise on write via `yaml.v3` but assert in a test).

### Step 2 — resolve chest references during `kart.new`

- In `internal/server/kart_new.go`, add `resolveTuneEnv(map[string]string)
  (map[string]string, error)` alongside `resolvePATSecret`. Reuse the
  same `chest.Get` path; on miss, return `chest_entry_not_found` with
  the offending key in `rpcerr.Data`.
- Surface the resolved map to `kart.New` via a new field on
  `kart.Flags` (or on the resolver output — `kart.Resolved` already
  holds Character, Tune, Features).
- No values leave the server handler until step 3; keep them in memory
  only, never logged.

### Step 3 — thread env into `devpod up`

- Add `SetEnv []string` to `devpod.UpOpts` symmetric with `SSHOpts`, and
  map to `--set-env KEY=VALUE` in `args()`.
- At `internal/kart/new.go:150`, populate `up.SetEnv` from the resolved
  env map (`KEY=VALUE`, stable order).
- Persist the set of env keys (NOT values) into the kart config so
  `kart.start`/`kart.restart` can re-resolve them from chest on re-up
  without the user re-specifying.

### Step 4 — re-apply on lifecycle verbs

- `kart.start` and `kart.restart` call `devpod up` under the hood — thread
  the same resolved env through. Re-read chest on each invocation so
  rotated secrets land on restart.
- `kart.delete` doesn't touch env; no change.

### Step 5 — (optional, deferred) `drift connect --env-from-tune`

A follow-up that pipes the same resolved env through
`devpod.SSHOpts.SetEnv` for session-scoped use cases. Out of scope for
this plan; the container-env path covers the motivating failure.

### Step 6 — integration test

- Mirror `integration/dotfiles_test.go` shape. Scenario:
  1. `chest.set github-pat <value>`
  2. Write a tune with `env: { GITHUB_TOKEN: chest:github-pat }`
  3. `kart.new` with that tune
  4. `devpod ssh <name> --command 'printenv GITHUB_TOKEN'` matches the
     value
- Plus a negative test: unresolvable `chest:missing` → `kart.new`
  returns `chest_entry_not_found`, no container left behind.

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
