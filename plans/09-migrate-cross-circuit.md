# drift migrate — cross-circuit transfer

## Problem

A drift kart lives on the circuit where it was `drift new`'d. When that
circuit goes away — decommissioned VPS, retired attic box, laptop swap —
or when the user's physical location shifts enough that the circuit is
now on the wrong continent (Osaka ↔ London trip, Proxmox → tailscale-
egress swap), there's no in-tree way to move the kart. Today the
workaround is: `drift new foo --clone <same url>` on circuit B, then
manually rebind character and tune and re-set chest refs. Tedious,
error-prone, and easy to forget a detail.

Plan 07 (`07-drift-migrate.md`) already covers devpod→drift adoption on
a single circuit. This plan extends the same command to a new shape:
`drift migrate --from=<circuit-a> --to=<circuit-b> <kart>` — a fully
declarative transfer driven entirely by the drift client talking to
two lakitus at once.

## Goals

1. Move a single drift kart from circuit A to circuit B with one
   command, preserving source (repo/starter), tune, character binding,
   and env-ref block wiring (by name, not resolved values).
2. Pure client orchestration: drift reads from A's RPC surface, writes
   to B's, and never asks either lakitu to know the other exists. The
   only thing that traverses both circuits is the client process.
3. Safe default: source kart is left intact unless the user opts in to
   post-transfer cleanup, and cleanup is a separate confirmable step
   after B is verified running.
4. Backward-compatible with plan 07's `drift migrate` (no `--from` /
   `--to` flags → existing devpod→drift adopt flow unchanged). The two
   modes dispatch off flag shape, not a new subcommand.

## Non-goals

- **Container state.** Uncommitted working-tree changes, installed
  packages, running processes — none of that moves. The kart is
  re-cloned fresh on B from the same repo URL. (This matches the
  policy in plan 07: repo-portable bits only.)
- **Chest-secret transport.** Env refs move by *name*; whether B's
  chest backend has a value for each ref is B's problem. If a ref is
  unresolvable on B, flag it in the client output but proceed — the
  kart will surface its own clearer error on first start.
- **Batch migration.** One kart per invocation. Users script the loop
  themselves if they need multiple.
- **Tune/character auto-creation on B.** If A's kart uses a tune or
  character that doesn't exist on B, migrate errors out with a clear
  "define it on B first or pick a substitute" message. Creating tunes
  and characters is already its own CLI surface; not migrate's job to
  duplicate it.
- **Docker volume migration.** Even if the kart has a meaningful state
  volume on A's docker daemon, migrating across docker daemons is a
  tar/rsync problem that belongs in a separate plan (if ever).
- **Running-kart transfer.** Source must be stopped (or A's kart info
  reports a stopped/notfound status) before transfer begins; otherwise
  migrate errors out to force the user's hand. Avoids racing state
  changes mid-copy.

## Architecture

### Invocation shapes

```
drift migrate                               # existing devpod→drift flow
drift migrate --from=<circuit-a> --to=<circuit-b> [<kart>]
drift migrate --from=<circuit-a> <kart>     # --to defaults to default circuit
```

Kong flag spec: `--from` / `--to` are optional strings; when `--from`
names a registered circuit, dispatch takes the cross-circuit branch;
otherwise the existing adopt flow runs. `<kart>` is optional — when
omitted, the client fetches `kart.list` from A and huh.Selects.

### Client orchestration

```
1. drift connects to A via the existing ssh RPC transport
2. kart.export on A  →  full portable kart config (see wire types below)
3. Resolve target:
     - tune.list  on B     → verify export.Tune exists; prompt substitute
     - character.list on B → verify export.Character exists; prompt substitute
     - chest.list on B     → warn (not fail) on unresolvable env refs
4. kart.info on B → confirm no name collision; if present, prompt
   rename (same pattern plan 07 uses for devpod→drift adopt).
5. huh.Confirm the transfer (summary: "A:foo → B:foo, tune=default,
   character=kurisu, 3 env.session refs, 1 unresolvable on B").
6. kart.new on B with the adapted params + migrated_from={circuit:A,
   kart:foo}. Standard create flow from there (clone, tune apply,
   devpod up, success).
7. On success: prompt "delete source kart foo on A? [y/N]". If y,
   kart.delete on A. Default is no — leaves the user with a working
   fallback on A until they're confident B is healthy.
```

Non-TTY stdin: cross-circuit migrate errors out (same as plan 07) —
destructive-ish and too many prompts to reasonably script without a
shape-specific flag set.

### Wire surface

One new server RPC:

| method | request | response |
|---|---|---|
| `kart.export` | `{name}` | `KartExportResult` |

```go
// internal/wire/kart_export.go (illustrative)
type KartExportResult struct {
    Name         string               `json:"name"`
    Source       KartSource           `json:"source"`         // same shape kart.info returns
    Tune         string               `json:"tune,omitempty"`
    Character    string               `json:"character,omitempty"`
    Autostart    bool                 `json:"autostart"`
    Env          KartEnvBlock         `json:"env,omitempty"`  // session / workspace / build, ref-only
    MigratedFrom *KartMigratedFrom    `json:"migrated_from,omitempty"`
}
```

Key points:
- Source uses the same `KartSource` type as `kart.info` — clone URL,
  starter URL, optional revision pin. No new type.
- Env block is ref names (`chest:<key>`), never resolved values —
  secrets stay on A.
- MigratedFrom is chained: if A's kart was itself migrated from C,
  B's new kart's `migrated_from` points at A (the direct predecessor),
  and `previous_migrated_from` captures the chain so auditability
  isn't lost.

Additive field on existing `kart.new` params:
- `migrated_from: {circuit, kart}` — new shape; plan 07's
  `{context, name}` form is kept for the devpod-adopt path, with the
  handler discriminating on which set of keys is present.

### kart.export handler

Trivial — reads the kart config YAML off disk, strips nothing, wraps
in `KartExportResult`. The server-side work is ~20 lines.

The handler is read-only, so it's fine to expose even when the kart
is running. Cross-circuit migrate enforces "source stopped" client-
side (plan step above) so that if B's `kart.new` fails midway, A's
kart is still in a clean state to retry against.

### Data model

`KartConfig.MigratedFrom` (already exists, from plan 07) grows one new
form:

```yaml
migrated_from:
  # When adopting a devpod workspace (plan 07):
  context: default
  name: research
  # OR when transferring cross-circuit (plan 09):
  circuit: osaka
  kart: research
```

Callers discriminate on which pair of keys is non-empty. YAML
tolerates the presence of both in a legacy config without erroring;
new writes only populate one pair.

The model can carry one optional nested `previous_migrated_from` to
preserve the chain across re-migrations — cap the depth at 5 levels
(anything deeper is almost certainly a user accident or a loop, and
the summary line gets unreadable past ~3).

## UX notes

- The transfer summary should surface everything the user might want
  to sanity-check before committing: source/target circuit names,
  kart name, tune, character, count of env refs (and warning count
  for unresolvable ones), and whether B's chest has every referenced
  key. All info from cheap RPCs (`tune.list`, `character.list`,
  `chest.list`).
- "Stopped source" precondition: after fetching `kart.info` from A,
  if `status == running`, print a clear "stop it first: drift --
  circuit=A stop <kart>". Don't auto-stop — user consent on
  destructive-ish operations stays explicit.
- Name collision on B: huh.Input with a placeholder that prefixes the
  source circuit (`<a>-<kart>`), same idiom as plan 07's adopt flow.

## Observability / failure modes

- **RPC to A fails mid-transfer (post-export, pre-B.kart.new):** no
  writes on either side, client aborts, no cleanup needed.
- **B.kart.new fails:** source kart on A is untouched. Client surfaces
  B's error verbatim and exits nonzero.
- **B.kart.new succeeds, B.kart.start fails:** drift already surfaces
  the kart as `error` on B's `drift list`; user can debug on B or
  `drift --circuit=B delete <kart>` and retry.
- **Post-success cleanup (kart.delete on A) fails:** surface as a
  warning, not a fatal — the transfer itself succeeded, cleanup is a
  follow-up the user can retry.

## New RPCs

| method | request | response |
|---|---|---|
| `kart.export` | `{name}` | full portable config (see above) |

Plus the additive `migrated_from` shape on `kart.new` params.

No other protocol changes; cross-circuit migrate leans on the existing
`kart.list`, `kart.info`, `kart.new`, `kart.delete`, `tune.list`,
`character.list`, `chest.list` RPCs on both ends.

## Compat / rollout

- Client on a newer drift than A's lakitu: `kart.export` comes back as
  `method_not_found`; the existing stale-lakitu hint kicks in with an
  actionable "upgrade lakitu on <circuit-a>" message. Transfer exits
  cleanly; no partial state.
- Client on a newer drift than B's lakitu: `kart.new` with the new
  `migrated_from` shape might fail validation on a lakitu that
  doesn't know the new keys. Mitigate by adding the shape under
  `DisallowUnknownFields` being off for this one field-group (or
  gated on the lakitu API version reported by `server.version`).
- Older drift against a newer lakitu: unaffected — older drift never
  calls `kart.export` and the handler is additive.

## Test plan

- Unit: `kart.export` handler returns faithful config; adapter
  validation surfaces missing tunes / characters / chest keys as
  user-visible warnings without blocking; `migrated_from` chain
  preservation across 2+ hops.
- Integration: standup two circuit-alpha-style nixos-containers in
  the integration harness; create a kart on one via `drift new`,
  `drift migrate --from=a --to=b` to the other, assert the kart is
  running on b and the `migrated_from` backref is populated. Harness
  gets a `StartTwoCircuits` helper alongside the existing
  `StartReadyCircuit`.
- Manual smoke on dev-proxmox: migrate a kart from the host circuit
  to the `circuit-alpha` container (both registered as drift circuits
  on the same workstation) to verify the full huh-driven flow end-
  to-end.

## Out of scope / follow-ups

- Automatic chest-secret transport between circuits. The right
  primitive is probably "chest.export + chest.import" driven by the
  client, with per-key confirmation; deserves its own plan.
- Docker-volume transfer for karts with meaningful state on disk.
- `drift rename` as a precursor — cross-circuit migrate's name-
  collision path would simplify if the client could trivially rename
  on B. Currently users delete-and-retry.
- Parallel batch migrate (`drift migrate --from=a --to=b --all` with
  a progress UI) — useful when retiring a circuit but not core.
