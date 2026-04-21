# drift migrate — adopt existing devpod workspaces

## Problem

Users with pre-existing devpod workspaces cannot adopt them into drift
today. They would have to `drift new` each workspace by hand, re-typing
the repo URL and picking a tune/character. For users with 30+ devpod
workspaces (the common case for anyone who was on devpod before drift
existed) this is prohibitively tedious and error-prone.

We also want to guarantee drift-managed workspaces never collide with the
user's existing devpod state: a user's `devpod list` / `devpod delete`
habits must keep working without any awareness of drift, and drift must
never accidentally touch the user's own workspaces.

## Goals

1. One interactive command, `drift migrate`, that adopts a single
   existing devpod workspace as a new drift kart per invocation.
2. Total namespace isolation between drift-managed and user-managed
   devpod workspaces — they live in separate devpod contexts on the
   circuit server.
3. Pure RPC orchestration: the client never reads `~/.devpod/` and never
   invokes devpod. The server does all devpod work via a bundled devpod
   binary of a known-pinned version.
4. Back-reference (`migrated_from`) persisted in the drift kart config,
   so repeated `drift migrate` runs hide already-migrated entries.

## Non-goals

- Batch migration. One kart per invocation; users run the command
  repeatedly for multiple karts.
- Adopting container state. Migration only captures the repo URL; the
  drift kart is re-cloned fresh from the tune's devcontainer. The old
  devpod workspace stays intact (optional cleanup prompt at the end).
- Non-git workspaces. Entries with empty `source.gitRepository` are
  filtered out — we have no deterministic way to reproduce them.
- Env-var preservation. The new kart starts from the tune's env refs;
  any env the user set inside the old container is not carried over.
- `drift rebuild` / `drift reset`. These are mentioned in the design
  discussion as useful follow-ups for re-applying tune changes to an
  existing kart; separate plans.

## Architecture

### Drift devpod context

All drift-managed workspaces live in a dedicated `drift` devpod context
on the circuit server (`~/.devpod/agent/contexts/drift/`). The user's
own contexts (`default`, plus any they've added) are untouched. Every
server-side devpod invocation passes `--context drift`.

On first run, the server creates the context and registers a provider in
it. A sentinel file (`~/.devpod/agent/contexts/drift/.drift-managed`)
identifies the context as drift's; if the context exists without the
sentinel, server refuses to proceed with a clear error.

### Bundled devpod + DEVPOD_HOME isolation (server only)

The server binary (`lakitu`) embeds a pinned devpod binary via
`go:embed`, extracted to `~/.drift/bin/devpod` on startup. It runs with
`DEVPOD_HOME=~/.drift/devpod/` so every drift-managed workspace lives
in a completely separate state tree from the user's `~/.devpod/`. The
user's `devpod list` / `devpod delete` operate on their own HOME and
literally cannot see drift's state. Client has no devpod dependency —
migrate works on clients with no devpod installed.

Migrate's candidate scanner always reads from the user's
`~/.devpod/agent/contexts/*/workspaces/*/workspace.json` — pure
filesystem reads, no binary invoked. The bundled devpod is only used
for drift's own operations (`kart.new`, lifecycle), against drift's
own HOME.

Bundling mechanics (`go:embed` + goreleaser vendoring) are an
implementation follow-up; the `DEVPOD_HOME` wiring lands first against
whatever devpod binary is on the server's `$PATH`, and the bundle
transparently replaces it later.

### Migrate flow (pure RPC)

Client is UI-only:

```
1. kart.migrate_list                          -> [{Name, Context, Repo}]
2. huh.Select  "<context>/<name>  <repo>"     -> pick one
3. tune.list, character.list                  (populate dropdowns)
4. huh.Select tune    (default: server's default_tune)
   huh.Select character   (default: server's default_character)
5. huh.Confirm
6. kart.new(name, repo, tune, character, migrated_from={ctx, name})
   └─ TypeNameCollision -> huh.Input "new name"
                            (suggestion: "<ctx>-<name>")
                            -> retry kart.new
7. Print the manual `devpod delete` recipe (with --context flag when
   non-default). Cleanup is the user's responsibility — drift never
   touches the user's ~/.devpod/ state.
```

Non-TTY stdin: migrate errors out immediately with a clear message. No
`--yes` escape hatch — this is an inherently interactive command.

### Server-side candidate filter (kart.migrate-list)

```
glob ~/.devpod/agent/contexts/*/workspaces/*/workspace.json
  drop contexts/drift/**                      (drift-owned, separate dir)
  drop entries with empty source.gitRepository
  drop entries whose {context, name} matches an existing drift kart's
       migrated_from back-reference
return []{Name, Context, Repo}
```

### Data model

`internal/server/KartConfig` gains one optional field:

```yaml
migrated_from:
  context: default
  name: research
```

Written only when the kart is created via `drift migrate`. Read for
dedup.

## New RPCs

| method | request | response |
|---|---|---|
| `kart.migrate_list` | — | `{Candidates: []{Name, Context, Repo}, DefaultTune, DefaultCharacter}` |

Plus one additive field on `kart.new` params:
- optional `migrated_from: {context, name}` — persisted on the kart's
  on-disk config so subsequent migrate runs can dedup.

`kart.new` already returns `rpcerr.TypeNameCollision` on name collision
— migrate client catches and reprompts for a new kart name.

Tune and character enumeration reuses the existing `tune.list` and
`character.list` RPCs; the CLI picks `.name` from each result. Server
defaults ride on the `kart.migrate_list` response so the dropdown's
pre-selection needs no extra round trip.

## Out of scope / follow-ups

- `drift rebuild <name>` — devpod `up --recreate`, preserves `content/`,
  re-applies tune's features/dotfiles.
- `drift reset <name>` — full wipe (delete + new); re-clones, re-applies
  everything. Requires git-clean preflight.
- `go:embed`-bundled devpod + per-arch goreleaser hook.
- Download-on-install devpod (smaller binary, network at first server
  start).
