# tune / character / chest — unified update surface + drift detection

## Problem

Two distinct gaps in the current config-object surface, both exposed while
wiring up a `~/.claude` mount on the `default` tune:

1. **`lakitu tune set` silently drops fields the CLI doesn't flag.** The
   handler full-replaces `garage/tunes/<name>.yaml` from the request
   params, but the CLI only carries `--starter` / `--devcontainer` /
   `--dotfiles-repo` / `--features`. `env` (chest refs) and `mount_dirs`
   have no flags, so re-running `tune set` to tweak one field writes an
   empty `env` and no `mount_dirs` back to disk. A chest-backed
   `GITHUB_TOKEN` or host bind set via YAML is wiped on the next edit.
2. **`lakitu character` has no edit surface at all.** Only `add` / `rm`
   / `list` / `show` — and `rm` is blocked whenever a kart references
   the character. To change a git email you'd have to delete every
   kart bound to the character first. In practice: hand-edit
   `garage/characters/<name>.yaml` and reload the server.

Underneath both is the same missing primitive: a patch-merge update
shape that touches only the fields the caller mentioned.

There's a third, related gap the first two make worse: **edits to a
tune or character don't propagate to karts created before the edit**.
The resolver captures the tune shape into `garage/karts/<name>/config.yaml`
at `kart.new`; lifecycle ops replay the captured config, not the live
tune. Even after we fix the update surface, a user who edits the
default tune has no signal that existing karts are now stale. This is
already tracked in the host `TODO.md` under "Kart-config idempotency
vs tune drift"; this plan folds the connect-time detection piece of it
in, since the two designs are entangled (you can't sensibly prompt for
rebuild if your update API is lossy).

## Goals

1. One verb discipline across every config object (tune, character,
   chest, kart): `new` creates, `set` patches one field, `unset`
   clears one field, `rm` deletes the object, `show` / `list` read,
   `edit` drops to `$EDITOR` for bulk work.
2. Patch-merge server-side: requests carry only the fields the caller
   meant to change, and omitted fields stay untouched on disk.
3. `git config`-shaped addressing — `set <name> <field.path> <value>`
   and `unset <name> <field.path>` — so every field is reachable from
   the CLI without inventing a new flag per field added to the model.
4. Connect-time drift detection on `drift connect`: if the kart's
   captured shape differs from the live tune / character in a way that
   requires a container rebuild, prompt once ("tune X changed —
   rebuild now? [y/N]"). Default: no, just connect.
5. Ship as a clean break — old method names and lossy semantics go
   away in the same release, no deprecation window.

## Non-goals

- **Tune/character versioning or history.** No commit log of edits,
  no rollback. If you want history, put the garage in git yourself.
- **List-field mutation primitives** (`add` / `remove` on
  `mount_dirs`). Lists go through `set` with a JSON value, or
  `tune edit`. If we find ourselves reaching for list helpers often,
  we'll add `git config --add` shape later; not now.
- **Automatic cascade on edit.** Editing a tune does not trigger
  rebuilds of referring karts. Drift is detected at `drift connect`
  time only (not on `drift list`, not on `kart.start`); nothing
  happens in the background.
- **Chest rotation tracking.** If `chest set GH_PAT` changes a value
  that kart X's `env.build.GITHUB_TOKEN` resolves to, we do not count
  that as drift. Chest values are dynamic at session-env resolution
  time, or stale-at-build for build-env — either way, not a rebuild
  trigger.
- **Ephemeral in-container state.** Rebuild blows away uncommitted
  working tree / installed packages / running processes. Same policy
  as everywhere else in drift; called out explicitly in the prompt.
- **Non-structural character edits.** Changing `git_name` / `git_email`
  affects files inside the container (gitconfig), but today the
  simplest path is "rebuild" same as any other change. A
  "soft-reapply" mode (sync gitconfig without recreating the
  container) is a follow-up, not this plan.

## Architecture

### Verb discipline

Applied uniformly across `tune`, `character`, `chest`, and `kart`:

| verb   | shape                                     | errors                  |
|--------|-------------------------------------------|-------------------------|
| `new`  | `<thing> new <name> [--flags…]`           | if `<name>` exists      |
| `set`  | `<thing> set <name> <field.path> <value>` | if `<name>` missing     |
| `unset`| `<thing> unset <name> <field.path>`       | if `<name>` missing     |
| `rm`   | `<thing> rm <name>`                       | if `<name>` missing OR in use |
| `show` | `<thing> show <name>`                     | if `<name>` missing     |
| `list` | `<thing> list`                            | —                       |
| `edit` | `<thing> edit <name>`                     | if `<name>` missing     |

`new` carries flags for the common creation-time fields; post-creation
mutations go through `set` / `unset` / `edit`. No "upsert" verb —
sharp boundary between create and update, so error messages are
always unambiguous ("already exists" vs "not found") and scripts can
rely on the semantics.

`kart` and `drift` already use `new`; changes are scoped to
`lakitu tune`, `lakitu character`, `lakitu chest`. `character add` →
`character new` (rename, no alias — early enough in adoption, not
worth carrying two verbs).

### Field addressing

`git config` shape: dotted path strings, one field at a time.

```
lakitu tune set default env.build.GITHUB_TOKEN chest:kurisu-pat
lakitu tune unset default env.build.GITHUB_TOKEN
lakitu tune set default devcontainer ./fragments/claude.jsonc
lakitu character set kurisu git_email new@example.com
lakitu chest set kurisu-pat           # value from stdin (scalar chest)
```

Server parses `<field.path>` against the model's YAML tags. Leaf must
resolve to a scalar (string, bool, int). Map children are addressable
(`env.build.GITHUB_TOKEN`); list elements are not (`mount_dirs[0]`
not supported — use `edit`).

Setting a map field that doesn't exist yet creates the intermediate
maps (`env` → `env.build` → `env.build.GITHUB_TOKEN`); `unset` on a
map leaf removes the leaf and prunes empty parents.

`value` type coercion follows the field's Go type: a `bool` field
accepts `true` / `false`, a string accepts the raw argument. Chest
refs (strings starting with `chest:`) pass through validation the
same way they do today at `tune.set` time — no special casing.

### CLI / RPC split

Client ergonomics versus server primitives:

| client CLI                                  | server RPC                     |
|---------------------------------------------|--------------------------------|
| `tune new <n> [--flags]`                    | `tune.new` (full struct)       |
| `tune set <n> <path> <val>`                 | `tune.patch` (single field)    |
| `tune unset <n> <path>`                     | `tune.patch` (unset form)      |
| `tune edit <n>`                             | `tune.get` + `tune.replace`    |
| `tune rm` / `show` / `list`                 | unchanged                      |

Same pattern for `character`. `chest` is simpler (flat scalars, no
paths — `set` is current semantics renamed; `new` errors if exists;
`set` errors if missing).

### `tune.patch` / `character.patch` RPC shape

```go
// internal/wire/tune_patch.go (illustrative)
type TunePatchRequest struct {
    Name  string        `json:"name"`
    Ops   []TunePatchOp `json:"ops"`
}

type TunePatchOp struct {
    Path  string      `json:"path"`          // e.g. "env.build.GITHUB_TOKEN"
    Op    string      `json:"op"`            // "set" | "unset"
    Value interface{} `json:"value,omitempty"` // omitted for unset
}
```

One RPC carries N ops so `tune edit` (which produces a diff against
current YAML) can submit everything in one atomic request.
Single-field `set` / `unset` from the CLI builds a one-op request.

Handler:

1. Read current YAML into `model.Tune`.
2. Apply ops in order; each op resolves the dotted path against the
   struct's YAML tags (reflection + `yaml.v3` node walk).
3. Re-run tune validation (`validateTuneEnv`, name checks).
4. `config.WriteFileAtomic` the full YAML back.

Map creation/pruning happens inside the apply loop; the pre-validated
struct is what gets marshalled, so we're never writing an ambiguous
YAML shape.

### `tune edit` flow

Thin wrapper around existing RPCs:

1. Client: `tune.get <name>` → YAML bytes.
2. Client writes to a tempfile, launches `$EDITOR` (fallback `vi`).
3. On editor exit, re-read tempfile; if unchanged, abort silently.
4. Client submits `tune.replace` (full-replace, new RPC — or reuse
   `tune.new` with an "allow exists" flag; pick whichever is cleaner
   at impl time) with the edited YAML. Server validates and either
   commits or returns a structured validation error that the client
   surfaces with "re-open editor? [Y/n]" (same `huh.Confirm` idiom as
   `drift migrate`).

Non-TTY stdin: `edit` errors out immediately ("editor requires a
TTY"), same policy as other interactive commands.

### Drift detection on `drift connect`

#### What counts as drift (structural)

Fields whose change requires a container rebuild:

- **tune**: `devcontainer`, `mount_dirs`, `features`, `dotfiles_repo`,
  `env.build.*`
- **character**: `ssh_key_path`, `pat_secret`

Fields whose change does **not** count as drift (can be re-applied
at start without recreating):

- **tune**: `env.workspace.*`, `env.session.*` (loaded at kart-start
  / session-enter)
- **character**: `git_name`, `git_email`, `github_user` (applied into
  gitconfig at session-enter; a `drift restart` is enough, and even
  then the kart will self-correct on next session)

Chest edits don't count regardless — see non-goals.

#### Snapshot + compare

The kart's resolved config already lives at
`garage/karts/<name>/config.yaml` (captured at `kart.new`). This plan
doesn't change that. What's new: a `kart.drift_check` RPC.

```go
type DriftCheckRequest  struct { Name string `json:"name"` }
type DriftCheckResult   struct {
    Drifted bool           `json:"drifted"`
    Fields  []DriftedField `json:"fields,omitempty"`
}
type DriftedField struct {
    Path     string      `json:"path"`     // e.g. "tune.mount_dirs"
    Source   string      `json:"source"`   // "tune" | "character"
    SourceName string    `json:"source_name"` // tune name / character name
    Was      interface{} `json:"was"`
    Now      interface{} `json:"now"`
}
```

Handler reads the captured kart config and the live tune / character,
walks the structural-field list above, and returns a structured diff.
Cheap — no container introspection; everything is YAML on disk.

#### Connect flow

`drift connect <name>` today resolves the kart, opens the ssh tunnel,
hands off to the shell. New preamble step:

```
1. kart.drift_check
   └─ drifted=false → proceed as today
   └─ drifted=true  → print one-line summary:
      "tune 'default' has changed since this kart was built:
         + mount_dirs: added /home/dev/.claude:/home/dev/.claude
         + env.build.GITHUB_TOKEN: chest:old-key → chest:new-key
       rebuild now? [y/N]"
      └─ n (default) → connect as today
      └─ y          → kart.rebuild, then connect
```

The summary lists drifted fields (name, before, after); if
`pat_secret` or other sensitive-looking fields differ, the values are
redacted to the chest-ref name (the raw secret is never in the diff
anyway, but double-check at the renderer).

One prompt per connect. No "remind me next time" state — the prompt
is cheap and explicit.

#### `kart.rebuild` RPC

Not invented in this plan; this is the same primitive the existing
"Kart-config idempotency" TODO bullet already calls `drift kart sync`.
Shape:

```go
type RebuildRequest struct { Name string `json:"name"` }
```

Handler re-resolves the kart's config from the current tune +
character, writes the new `garage/karts/<name>/config.yaml`, and
recreates the devcontainer (devpod `up --recreate` against drift's
`DEVPOD_HOME`). Existing kart-start error plumbing carries failure
modes verbatim; client surfaces them as "rebuild failed: …" and
aborts the connect.

This RPC is reusable: a future `drift kart rebuild <name>` client
verb could drive it explicitly, without the drift-detection dance.
Scoped as part of this plan because connect-time rebuild is useless
without it.

### Non-TTY `drift connect`

If the drift-check reports drift and stdin is non-TTY, do not prompt.
Print a one-line warning ("tune 'default' has drifted — run `drift
kart rebuild <name>` to apply") and connect. Same principle as other
interactive paths: never block a script.

## UX notes

- `set` output is silent on success, same as today's `tune set` /
  `chest set`. One-liner on change ("set tune.default.env.build.GITHUB_TOKEN").
- `unset` prints a one-liner noting the cleared path; no-op unset
  (field already absent) exits 0 with "already unset" for scriptability.
- `new` flags match the current `tune set` flags verbatim — no new
  flag surface, just moved to a create-only verb. Any field not
  coverable by a flag (`env`, `mount_dirs`) is reachable via `set`
  after creation.
- `edit` prints the tempfile path on validation failure so the user
  can reopen it without losing work; same pattern as `git commit`
  failing on a pre-commit hook.
- `drift connect` drift prompt is the only connect-time addition;
  the summary line fits in one terminal height even with half a dozen
  drifted fields.

## Observability / failure modes

- **`set` on a stale kart reference** (tune being edited has karts
  bound to it): succeeds — the edit is to the tune, not the karts.
  Drift is surfaced later at connect.
- **Concurrent `set` / `edit` on the same object**: `config.WriteFileAtomic`
  handles atomic replace of the YAML. Last writer wins; no lock.
  If users want serialisation they can script it. (Same semantics as
  today.)
- **`drift_check` RPC fails mid-connect**: treated as "assume no
  drift" — log the error, proceed to connect. Never block a connect
  on a diagnostic.
- **`kart.rebuild` fails after user consented**: surface the server
  error verbatim, exit non-zero, do not attempt the connect. User's
  kart is in whatever state the rebuild left it (devpod reports);
  next connect will re-check drift and re-offer rebuild.
- **Invalid field path on `set` / `unset`**: structured error with
  the parsed-up-to segment and a hint at what YAML tags exist at
  that level ("unknown field `env.buld` — did you mean `env.build`?").

## New RPCs

| method              | request                     | response            |
|---------------------|-----------------------------|---------------------|
| `tune.new`          | full struct                 | `{ok}`              |
| `tune.patch`        | `{name, ops[]}`             | `{ok}`              |
| `tune.replace`      | `{name, yaml}`              | `{ok}`              |
| `character.new`     | full struct                 | `{ok}`              |
| `character.patch`   | `{name, ops[]}`             | `{ok}`              |
| `character.replace` | `{name, yaml}`              | `{ok}`              |
| `chest.new`         | `{name, value}`             | `{ok}`              |
| `chest.patch`       | `{name, value}`             | `{ok}`              |
| `kart.drift_check`  | `{name}`                    | `DriftCheckResult`  |
| `kart.rebuild`      | `{name}`                    | `{ok}`              |

Deleted: `tune.set` (replaced by `tune.new` + `tune.patch` +
`tune.replace`), `character.add` (→ `character.new`), `chest.set`
(→ `chest.patch`).

Renames: `tune.set` → `tune.patch` + `tune.new` + `tune.replace`
(the old lossy `tune.set` method is deleted, not aliased);
`character.add` → `character.new`. Client and server ship together so
the rename is atomic — no alias, no deprecation window.

`chest.set` is renamed to `chest.patch` for symmetry (still a scalar
set, just consistent verb). The new `chest.new` wraps it with an
"errors if exists" precondition.

## Rollout

Client and server versions are pinned to each other — drift and
lakitu from the same release are assumed. Mixed-version behaviour
isn't a goal; a too-old lakitu surfaces `method_not_found` from the
new RPCs and the existing stale-lakitu hint ("upgrade lakitu on
<circuit>") takes it from there.

Drift detection works retroactively: karts created before this change
already have `garage/karts/<name>/config.yaml` captured at `kart.new`
(existing behaviour), so `kart.drift_check` has both sides of the
comparison on day one.

## Test plan

- Unit:
  - `tune.patch` handler: set/unset on every field type in
    `model.Tune`; map creation + pruning; reject paths past scalar
    leaves; reject unknown paths with the "did you mean" hint;
    chest-ref validation still fires on `env.*` sets.
  - Field-path resolver against reflection of `model.Tune` and
    `model.Character`: ensure every YAML-tagged field is reachable.
  - `kart.drift_check`: all structural fields detected; non-structural
    fields ignored; no false positives on string-normalisation.
- Integration:
  - `lakitu tune new` → `set env.build.GITHUB_TOKEN chest:foo` →
    `show` reveals the chest ref → `unset` → `show` confirms cleared.
  - `drift connect` against a kart whose tune has drifted: prompt
    appears, "n" bypasses, "y" rebuilds and connects. Non-TTY path
    prints warning and connects without prompt.
  - Round-trip `tune edit` on a tune with `mount_dirs`: editor saves
    unchanged → no-op; editor adds a mount → persisted; editor
    produces invalid YAML → validation error surfaces tempfile path.
- Manual smoke on dev-proxmox:
  - Repro the original bug: `tune new`, `set mount_dirs` via `edit`,
    `set --starter` via flag — verify `mount_dirs` survives. Confirms
    the fix for the TODO item this plan opens with.

## Out of scope / follow-ups

- **Soft-reapply for non-structural fields** — a `kart.restart` path
  that re-loads `env.workspace` / `env.session` / gitconfig without
  recreating the container. Makes drift detection gentler for
  character edits.
- **List mutation helpers** (`tune add mount_dirs /host:/container`,
  `tune rm mount_dirs /host:/container`). Punt until we see users
  reach for `edit` specifically for list tweaks.
- **Batch `set` from file** (`lakitu tune set default --from-file x.yaml`
  — patch-merge from a partial YAML). Useful for declarative config
  management; easy to add once `tune.patch` exists.
- **Drift surfacing on `drift list`** — option (b) from the existing
  "Kart-config idempotency" TODO bullet. Passive warning column next
  to status. Reasonable follow-up once the connect-time prompt has
  bedded in.
- **Rebuild with state preservation** (named-volume promotion, or
  `devpod up --recreate` variants that preserve specific paths).
  Significant design surface; separate plan.
