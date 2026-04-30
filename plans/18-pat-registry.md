# PAT registry

## Problem

Today the only way to give a kart a GitHub PAT is to set `pat_secret: chest:<name>` on the character. That's fine when one identity owns one PAT, but real users have *several* tokens per identity: a long-lived broad-scope token for personal repos, a fine-grained read-only one for a specific org, a CI-shaped one with workflow scope, etc. The character-only model forces an awkward one-of-many choice at character-create time and offers no answer for "use the right token for this clone URL."

The chest stays the right place for the secret material. What's missing is a thin layer above it: a PAT object that knows *what the token is for* (owner, repos, expiration, regenerate link), so `kart new` can auto-pick the most-specific match and so the user has a single surface to inspect and rotate tokens before they expire.

## Goals

1. First-class `pat` server objects, separate from `chest` entries; the chest keeps storing the literal token, the pat object stores the metadata around it (owner, repo list, perms, expiration, regenerate URL, chest ref).
2. Interactive resolution at `kart new` time: when the clone URL matches one or more registered pats, the user is shown a picker (with PAT details and an explicit "skip / no PAT" option) regardless of how many candidates matched. Single match is *not* auto-picked — the user always confirms.
3. Per-kart override stays available so a user can force a specific pat (or none) regardless of registry state. PATs remain usable in other contexts (env refs, manual chest references) for users who don't want the registry layer.
4. Registration-only entry: the only way to attach a PAT to a kart is to reference a registered slug. Raw `github_pat_…` / `ghp_…` literals are rejected at the CLI; users must run `drift pat new` first. This keeps the chest as the single secret-storage surface and means kart YAMLs never carry token material.
5. UI affordances: `drift pats` shows expiration and a one-click regenerate link; expiry warnings surface in `lakitu list` / `kart info` once a token is within 14 days of expiring.

## Non-goals

- **Granting or rotating tokens automatically.** The registry tracks user-asserted metadata. GitHub stays the source of truth for what a token can actually do; a wrong scope record just means a clone 401s, not a security hole.
- **Replacing the chest.** PATs reference a chest entry by name; nothing about the secret-storage backend changes.
- **Classic PATs.** `ghp_*` tokens are rejected at registration (`internal/server/pat.go`). Only fine-grained PATs (`github_pat_*`) are accepted — they're the case the paste-driven flow actually solves, since fine-grained PATs are the only tokens whose expiration date is settings-page-only. Classic PATs would carry their own design baggage (different scope model, no `Access on @owner` line in the settings page) and we'd rather not support a second token shape just to support a second token shape.
- **Multi-account juggling.** A pat is owned by exactly one GitHub login. Cross-account routing (one kart, two pats from different accounts on the same clone) is out of scope for v1.

## Why paste-driven registration

The killer fact: GitHub's REST API does not expose a fine-grained PAT's expiration date to the token holder. There is no `/me/tokens/<id>` endpoint that returns `expires_at`. The settings page is the only place the user can see "Expires on Wed, May 27 2026," and the pat registry is useless for rotation reminders without that field. So the registration flow has to ingest the human-readable settings text one way or another.

Example paste (from `https://github.com/settings/personal-access-tokens/<id>`):

```
<token-name>

No description

Created on Mon, Apr 27 2026.
Expires on Wed, May 27 2026
Access on @<owner> <owner>
Repository access
<owner>/<repo>
User permissions
This token does not have any user permissions.
Repository permissions
 Read access to code and metadata
```

The parser is forgiving by design: it looks for line patterns (`Expires on …`, `Access on @…`, blocks under `Repository access`, `Repository permissions`) and records what it finds; missing fields are recorded as unknown rather than rejecting the paste. If GitHub reflows the page tomorrow, the user falls back to filling fields manually instead of being locked out. The literal token is still entered separately (or via `chest:<name>` reference) since the settings page deliberately does not show it after creation.

Live `/user` probe at registration time stays useful as a sanity check: the API confirms the token is valid and reveals the owner login, which we can compare against the pasted `Access on @…` line. A mismatch is a warning, not a hard error.

## Object shape

```yaml
# garage/pats/<slug>.yaml
slug: example-read-write
chest_ref: chest:example-read-write
name: example-read-write        # display name from the paste body
description: ""                  # nullable
owner: <owner>                   # "Access on @<owner>" or "Access on the @<org>"
expires_at: 2026-05-27           # nullable
created_at: 2026-04-27           # nullable
scopes:
  repos:                         # repo names the user asserts the token covers
    - <owner>/<repo>
  repos_all: false               # true ⇔ paste said "all repositories"
  perms:                         # user-asserted, free-form labels mirroring GitHub UI
    - "contents: read"
    - "metadata: read"
  user_perms: []
```

Fine-grained-only by design (see Non-goals). The token literal lives in the chest entry referenced by `chest_ref`; the YAML never stores it. A future regenerate-link affordance will need a `github_id` (parsed out of the settings URL the user pasted from); not in this slice.

## Resolution at `drift new`

Three sources, evaluated in order. The first one that yields a definite answer wins.

1. **Explicit flag.** `drift new <kart> --pat=<slug>` selects a registered pat by slug; `--pat=none` opts out of any PAT for this kart. Either form short-circuits the rest of the chain. The flag value must be a slug from `drift pats`; raw token strings are rejected at flag-parse time with a message that points at `drift pat new`.
2. **Auto-detect from clone URL.** When `--clone https://github.com/<owner>/<repo>` is present and step 1 didn't fire, lakitu scans the registry for pats whose `scopes.repos` list contains a literal `<owner>/<repo>` or owner-wildcard `<owner>/*` match (or whose `repos_all` is true and `owner == <owner>`). The candidate set, ranked by longest time-to-expiry first then most-recently-created, is returned to drift, which renders an interactive picker. The picker is shown unconditionally when the candidate set is non-empty — even for a single match — and always offers "skip / no PAT" as the last option. Each row shows: slug, name, owner, expires_at (with days-remaining), and a short repos summary (first match plus `+N more`). If the candidate set is empty, the picker is suppressed entirely and resolution falls through.
3. **Character fallback.** If steps 1 and 2 both produced no PAT, lakitu reads `character.pat_secret` (when a character is selected) and uses that. If that's also unset, no token is injected; the clone proceeds anonymously and may 401 for a private repo, in which case the error message points at `drift pat new`.

When `--clone` is absent, step 2 is skipped — there's no repo to match against, so resolution goes straight from step 1 to step 3.

The picker policy is deliberately heavier than plan 18's earlier "specificity ranking" lean: ambiguity on a security-relevant field is always worth a half-second of human attention, and the explicit-confirm step doubles as a "wait, that PAT expires next week" reminder at the moment the kart is being wired up.

### Persisted shape on the kart

The kart YAML records `pat_slug: <slug>` (or omits it for `--pat=none` / no match). Resolution to the chest entry happens at every dotfiles-write — `pats/<slug>.yaml → chest_ref → chest dechest` — so re-warming a kart picks up rotations to the underlying chest entry automatically.

The tradeoff: renaming or deleting a PAT slug after a kart was created with it will break that kart's PAT injection on re-warm. To protect against accidental orphaning, `drift pat delete <slug>` refuses when any kart references the slug, with a list of the dependent karts and a hint to clear them via `drift kart pat clear <kart>` (deferred — see CLI surface below). Renaming is not supported; `drift pat new` with a different slug + `drift pat delete` of the old one is the migration path.

We deliberately don't store the resolved chest_ref alongside the slug. Storing both means two sources of truth that drift apart whenever the PAT's chest entry is rotated; storing only the slug makes the registry the authority and keeps the kart record self-describing.

## CLI surface

Shipped (commit ba4671e):

```
drift pat new [<slug>]            # interactive paste flow; derives slug from paste body when omitted
drift pat update <slug>           # rotate token and/or update scopes
drift pat delete <slug>           # drops both metadata and chest entry
drift pats                        # list view (slug, name, owner, repos count, expires, days-to-expiry)
```

This slice adds:

```
drift new <kart> --pat=<slug>     # force a specific registered PAT
drift new <kart> --pat=none       # skip PAT resolution entirely for this kart
drift new <kart> --clone <url>    # triggers the auto-detect picker when matches exist
```

`drift kart info` gains a `pat` row showing slug, owner, expires_at, days-remaining, and the regenerate URL when the underlying PAT has a `github_id`. `drift list` / `lakitu list` gains a soft-warning glyph next to any kart whose resolved PAT is within 14 days of expiry.

### Deferred to a follow-up plan

- `drift kart relink-pat <kart>` / `drift kart pat clear <kart>` — re-resolve or unset a kart's PAT after the fact. Useful but not blocking; for now, `drift pat delete` refusing on referenced slugs is enough to keep users out of trouble.
- `drift pat probe <slug>` — `/user` validity check. The registration flow already calls `/user` once at create-time; a standalone probe is mostly handy for "is this still valid?" after a long idle, which the expiry-warning column already approximates.
- `drift pat adopt <character>` — explicitly dropped, not deferred. With registration-only entry, the character's `pat_secret` keeps working as a legacy catch-all but no migration shortcut is needed; users register the PAT through `drift pat new` like any other.

## Open questions

- **Glob syntax.** Is `<owner>/*` enough or do we want full glob (`<owner>/foo-*`)? Start with literal repo names plus `<owner>/*`; revisit once a real user hits the limit.
- **Token-id capture without the URL.** Is there a parseable cue in the paste body that includes the id? Currently the only reliable source is the settings URL the user pasted from; we may need a small "and the URL of this token, please" prompt during `drift pat new`.

## Closed (decided)

- **Multiple matches at the same specificity.** *Decided: always show the picker.* Even a single match goes through the picker, with "skip / no PAT" as the last option. The picker is the disambiguation surface; specificity ranking is reduced to ordering candidates within the picker rather than auto-picking.
- **Character migration via `pat adopt`.** *Decided: dropped.* Registration via `drift pat new` is the only entry path; existing characters with `pat_secret: chest:<ref>` keep working untouched as a legacy catch-all, but no one-shot adoption command is shipping.
- **Persisted shape on the kart.** *Decided: `pat_slug` only.* The slug resolves through the registry to the chest at every dotfiles-write. `drift pat delete` refuses when karts reference the slug, which is what keeps us honest about durability.

## Out of scope for v1, candidates for follow-ups

- GitHub App tokens (different auth shape; the `/installation/repositories` endpoint applies there but not to PATs).
- Org-level routing rules ("any kart cloning from `<owner>` should use pat X").
- Auto-rotation via the GitHub API's PAT regeneration endpoint — fine-grained PATs cannot be regenerated programmatically without re-auth.
- A web UI for the paste flow. CLI-first, paste-via-`$EDITOR` is enough for v1; a TUI or web surface is a separate plan.
