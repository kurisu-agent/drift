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

### Bundled devpod (server only)

The server binary (`lakitu`) embeds a pinned devpod binary via
`go:embed`, extracted to `~/.drift/bin/devpod` on startup. Client has no
devpod dependency — migrate works on clients with no devpod installed.

This is an implementation follow-up separate from this plan; until it
lands, the server uses `$PATH` devpod with a version preflight.

### Migrate flow (pure RPC)

Client is UI-only:

```
1. kart.migrate-list                          -> [{Name, Context, Repo}]
2. huh.Select  "<context>/<name>  <repo>"     -> pick one
3. kart.list-tunes, kart.list-characters      (parallel)
4. huh.Select tune    (default: server's default_tune)
   huh.Select character   (default: server's default_character)
5. huh.Confirm
6. kart.new(name, repo, tune, character, migrated_from={ctx, name})
   └─ TypeNameCollision -> huh.Input "new name"
                            (suggestion: "<ctx>-<name>")
                            -> retry kart.new
7. huh.Confirm "delete old devpod workspace <ctx>/<name>?"  (default NO)
8. kart.migrate-delete-old(ctx, name) if yes
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
| `kart.migrate-list` | — | `[]{Name, Context, Repo}` |
| `kart.list-tunes` | — | `[]string` |
| `kart.list-characters` | — | `[]string` |
| `kart.migrate-delete-old` | `{Context, Name}` | ok / err |

Plus updates to `kart.new`:
- optional `migrated_from: {context, name}` field in params
- returns `rpcerr.TypeNameCollision` (already exists) on name collision
  — migrate client catches and reprompts.

## Out of scope / follow-ups

- `drift rebuild <name>` — devpod `up --recreate`, preserves `content/`,
  re-applies tune's features/dotfiles.
- `drift reset <name>` — full wipe (delete + new); re-clones, re-applies
  everything. Requires git-clean preflight.
- `go:embed`-bundled devpod + per-arch goreleaser hook.
- Download-on-install devpod (smaller binary, network at first server
  start).
