# drift ai / drift skill — split Claude launchers

## Problem

Two of the built-in `runs.yaml` entries exist only to launch claude with
different flavors of system prompt:

- `drift run ai` — bare `claude --dangerously-skip-permissions` in
  `~/.drift/`.
- `drift run scaffolder` — same claude invocation, but with
  `recipes/scaffolder.md` appended as a system prompt and a
  `connect-last-scaffold` post-hook so the client auto-connects into the
  kart claude just created.

This pattern wants to generalise. A drift circuit that has several
`~/.claude/skills/<skill>/` definitions (security-review, init,
scaffolder, review, …) should be reachable from the workstation without
editing `runs.yaml` every time a new skill lands, and without
hand-writing `claude --append-system-prompt "$(cat …)"` incantations.
`scaffolder` in particular is already a Claude skill on the circuit —
the runs.yaml entry is duplicative.

## Goals

1. Two dedicated subcommands, each doing one thing well:
   - `drift ai` — bare claude REPL on the circuit. No args, no flags
     beyond the shared `--ssh` / `--forward-agent` transport toggles.
     Direct replacement for `drift run ai`.
   - `drift skill` — list available skills. `drift skill <name>`
     invokes one with auto-prefixed prompt, replacing
     `drift run scaffolder` and any future skill-shaped run.
2. Skills are the source of truth, not runs. `drift skill` reads the
   circuit's `~/.claude/skills/` directory and lists every skill
   discoverable by its frontmatter (name + description). No drift-side
   registry duplication for the common case.
3. Auto-prefix: the user's prompt is wrapped so claude reliably invokes
   the named skill. The user never types "use the X skill" themselves.
4. Drop the built-in `ai` and `scaffolder` entries from the embedded
   `runs.yaml`. Delete `CLAUDE-scaffolder.md` and its embed (the skill
   lives under `~/.claude/skills/scaffolder/` on the circuit now). User-
   installed `runs.yaml` files are preserved untouched — drift doesn't
   edit them.
5. Keep the auto-connect handoff. Any `drift skill` session that writes
   `~/.drift/last-scaffold` on exit causes the client to `drift connect
   <kart>`, same as today. Becomes a generic handoff, not scaffolder-
   specific. (`drift ai` is bare claude with no post-hook.)

## Non-goals

- **Workstation-side skill discovery.** Skills on the user's laptop are
  irrelevant — claude runs on the circuit, so skills must live there.
  If a user wants a skill on their circuit, they copy it into
  `~/.claude/skills/` on the circuit (out of scope for drift).
- **Skill authoring / editing from drift.** `drift skill` is dispatch
  only. Creating or editing skills stays a file-edit operation on the
  circuit.
- **Per-skill declarative post-hooks.** The only post-hook that exists
  today is `connect-last-scaffold`. Generalising it to "any skill can
  declare a handoff" is speculative — keep the single sentinel-file
  convention and revisit when a second hook earns its place.
- **Migrating `runs.yaml` in place.** Users who already have `ai` /
  `scaffolder` entries in their server-side `~/.drift/runs.yaml` keep
  them — `drift run ai` still works via the generic dispatcher. We just
  stop *seeding* those entries on fresh installs.
- **Streaming / non-interactive output mode.** Both commands are
  always interactive (mosh/ssh TTY handoff to claude). No output-mode
  variant.

## Architecture

### Invocation shapes

```
drift ai                            # bare claude REPL on the circuit
drift ai --ssh                      # force ssh over mosh
drift ai --forward-agent            # ssh -A

drift skill                         # print skills table (no prompt on TTY);
                                    # with stdin TTY + no --output json, drops
                                    # into picker + prompt flow
drift skill <name>                  # prompt for input, run
drift skill <name> "<prompt>"       # one-shot
drift skill --ssh <name> …          # force ssh over mosh
```

Kong: two commands in `internal/cli/drift/drift.go`:

- `AI aiCmd` — fields: `SSH bool`, `ForwardAgent bool`. No positional
  args.
- `Skill skillCmd` — fields: `Name string (arg optional)`,
  `Prompt []string (arg optional passthrough)`, `SSH bool`,
  `ForwardAgent bool`. Sibling listing subcommand via `drift skill`
  with no args (dispatched the same way `drift run` with no args falls
  through to a listing).

Dispatch cases:

- `"ai"` → `runAIExec` — builds a client-side bare claude command, no
  RPC needed beyond circuit resolution. Keeps the command stable across
  drift releases without requiring a lakitu upgrade.
- `"skill"` → `runSkillList` (table on non-TTY; picker → prompt → run
  on TTY).
- `"skill <name>"`, `"skill <name> <prompt>"` → `runSkillExec` (resolve
  via `skill.resolve`, run).

### Skill discovery (server-side)

New RPC `skill.list` on lakitu. Walks `~/.claude/skills/*/SKILL.md`,
parses YAML frontmatter for `name` + `description`, returns
`[]wire.Skill{Name, Description}`. Missing directory → empty list, not
an error (circuit may not have claude configured yet).

Wire types go in `internal/wire/skill.go` — new file, parallels
`internal/wire/run.go`.

The server does no filtering; the client picks. Keep the RPC thin so a
future `lakitu skill list` CLI reuses the same handler.

### Command rendering

Two options considered:

- **A. Client builds the shell command inline.** Client receives the
  skill name from the picker, renders `cd "$HOME/.drift" && exec claude
  --dangerously-skip-permissions "<prefix><prompt>"`, hands to ssh/mosh.
  Cons: remote command shape baked into the client; any future tweak
  (system-prompt prefix, additional flags) ships a new drift binary.
- **B. Server resolves via new `skill.resolve` RPC.** Client sends
  `{skill, prompt}`, server returns `{command, mode, post}` just like
  `run.resolve`. Cons: one more handler; pro: server owns the "how to
  invoke claude with a skill" contract, so drift stays a thin client.

**Pick B** for `drift skill`. It matches the existing run pattern and
lets lakitu evolve the prefix wording without a client release. The
resolved command looks roughly like:

```sh
cd "$HOME/.drift" && \
rm -f last-scaffold && \
exec claude --dangerously-skip-permissions '<prefix><prompt>'
```

`<prefix>` is a short imperative the server renders — e.g. `"Use the
<skill> skill. "` — that nudges claude to dispatch through its Skill
tool. Exact wording is an implementation detail; unit-test it against
a known-good prefix in `internal/server/skill_test.go`.

`drift ai` by contrast is a fixed, unparameterised command; the client
renders `cd "$HOME/.drift" && exec claude --dangerously-skip-permissions`
directly and skips the resolve RPC round-trip.

### Post-hook

Reuse the existing `~/.drift/last-scaffold` sentinel for `drift skill`.
`skill.resolve` always returns `post: connect-last-scaffold`. On client
exit, read the sentinel; if non-empty, `drift connect <kart>`; if empty
or missing, exit cleanly.

`drift ai` has no post-hook. A user who wants bare claude with auto-
connect can invoke scaffolder explicitly via `drift skill scaffolder`.

This generalises the handoff from scaffolder-specific to any-skill: the
security-review skill doesn't write the sentinel and nothing happens;
the scaffolder skill does and the user lands in the new kart. The
sentinel name stays `last-scaffold` for continuity — renaming is a
later cleanup.

### Interactive picker

Reuse `pickAndFillRun`'s huh.Select UX — factor out a small helper (or
duplicate, if generics hurt readability) so `drift run` and
`drift skill` pickers render the same filterable list with name +
description. The prompt collection step is simpler than run's
per-arg-spec loop: a single `huh.NewText()` with title "Prompt for
<skill>".

### Top-level menu

Replace the two entries in `internal/cli/drift/menu.go`:

```
{key: "run › ai         — launch claude on the circuit", argv: []string{"run", "ai"}},
{key: "run › scaffolder — AI-scaffold a new project + kart", argv: []string{"run", "scaffolder"}},
```

with:

```
{key: "ai             — launch claude on the circuit", argv: []string{"ai"}},
{key: "skill          — list + invoke a Claude skill on the circuit", argv: []string{"skill"}},
```

### Help + docs

- `internal/cli/drift/help.go`: replace the `run ai` / `run scaffolder`
  bullets with `drift ai` and `drift skill` sections.
- `internal/config/CLAUDE.md` (circuit-side agent context): drop the
  "user launched you via `drift run ai`" preamble mention, replace with
  "launched via `drift ai` or `drift skill <name>`". Keep the rest.
- README.md / any top-level doc referencing `drift run ai` or
  `drift run scaffolder`.

## What to drop

From `internal/config/runs.yaml`:

- Remove the `ai:` entry (lines 33–36).
- Remove the `scaffolder:` entry (lines 38–51).

From `internal/config/runs_yaml.go`:

- Delete `embeddedScaffolderRecipe` embed (lines 13–14).
- Delete `EnsureScaffolderRecipe` (lines 40–47).
- Delete the embedded `internal/config/CLAUDE-scaffolder.md`.

From the lakitu init flow: remove the `EnsureScaffolderRecipe` call
(find it via grep; likely in `internal/server/init.go` or wherever
`EnsureRunsYAML` is invoked).

Existing user state untouched:

- Circuits with `~/.drift/runs.yaml` pre-populated from an older
  install keep their entries — `drift run ai` still dispatches via the
  generic run pipeline. The embedded seed only applies to fresh
  installs.
- `~/.drift/recipes/scaffolder.md` on already-provisioned circuits
  stays in place; users can delete it by hand once they've moved to the
  skill. drift doesn't touch it.

## Execution checklist

- [ ] `internal/wire/skill.go` — types: `Skill`, `SkillListResult`,
      `SkillResolveParams`, `SkillResolveResult`; method constants
      `skill.list`, `skill.resolve`.
- [ ] `internal/server/skill.go` — handlers: walk
      `~/.claude/skills/*/SKILL.md`, parse frontmatter, render command
      via text/template with skill + prompt. Unit tests against a
      fixture skills dir.
- [ ] Register RPC methods in the lakitu JSON-RPC dispatcher (find
      where `run.list` / `run.resolve` are wired; add alongside).
- [ ] `internal/cli/drift/ai.go` — new file. `aiCmd`, `runAIExec`:
      circuit resolve, build fixed bare-claude command, ssh/mosh
      handoff (reuse `buildRunArgv`).
- [ ] `internal/cli/drift/skill.go` — new file. `skillCmd`,
      `runSkillList` (print table → optional pick → prompt → run;
      scripted callers get table-only), `runSkillExec`, `pickSkill`,
      prompt collection, ssh/mosh handoff, post-hook to
      `connectLastScaffold`.
- [ ] `internal/cli/drift/drift.go` — add `AI aiCmd` and `Skill
      skillCmd` fields. Dispatch cases: `"ai"`, `"skill"`,
      `"skill <name>"`, `"skill <name> <prompt>"`.
- [ ] `internal/cli/drift/menu.go` — replace the two `run › …` AI
      entries with `ai` and `skill` entries.
- [ ] `internal/cli/drift/help.go` — update the command reference.
- [ ] `internal/config/runs.yaml` — drop `ai` and `scaffolder` entries.
- [ ] `internal/config/runs_yaml.go` — drop `embeddedScaffolderRecipe`,
      `EnsureScaffolderRecipe`.
- [ ] `internal/config/CLAUDE-scaffolder.md` — delete.
- [ ] Remove the `EnsureScaffolderRecipe` call site in lakitu init.
- [ ] `internal/config/CLAUDE.md` — update the "user launched you via
      `drift run ai`" preamble.
- [ ] README + any other doc hits for `drift run ai` /
      `drift run scaffolder` (grep).
- [ ] `integration/` — add a skill-dispatch smoke test if the existing
      `drift run ai` integration test is going away; otherwise adapt it.
- [ ] Manual verification on a real circuit: `drift ai` bare launch,
      `drift skill` picker, `drift skill scaffolder` round-trip with
      auto-connect, `drift skill security-review "scan the auth
      middleware"` one-shot.

## Open questions

- **Skill invocation prefix wording.** Picking a prefix that makes
  claude reliably route to the named skill without being redundant on
  skills that already auto-trigger. Prototype against three skills
  (scaffolder, security-review, review) and pick the shortest prefix
  that works for all three.
- **Should `drift skill` with no skills on the circuit show a setup
  hint?** E.g. "no Claude skills on this circuit — drop SKILL.md files
  into ~/.claude/skills/<name>/ to get started." Likely yes; cheap.
- **Sentinel rename.** `last-scaffold` → `last-handoff` is cleaner now
  that any skill can use it. Cross-version compatibility (older claude
  sessions still writing `last-scaffold`) says defer the rename to a
  follow-up with a brief transition period.
