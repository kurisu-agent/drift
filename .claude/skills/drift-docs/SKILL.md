---
name: drift-docs
description: Refresh drift's user-facing docs so they match the actual CLI. Regenerates `docs/drift-cli.md` as an exhaustive per-subcommand `--help` reference, keeps the README's "Commands" section and quickstart in sync with reality, and ensures the shorthand/cheat-sheet snippet near the top of README.md reflects the current surface. Invoke whenever the user says "update the docs", "sync the README", "regenerate the CLI reference", "docs are stale", after CLI flags or subcommands change, or when preparing a release. Prefer this skill over ad-hoc editing — it's what keeps the docs and the binary from drifting apart.
---

# drift-docs

The drift CLI is the source of truth. Docs exist to serve humans reading them;
they're only useful if they match what the binary actually does. This skill's
job is to close that gap: build the CLI, dump `--help` for every subcommand,
and reconcile the README and `docs/drift-cli.md` against the result.

## What "up to date" means

Three artifacts, in order of authority:

1. **The `drift` binary's own `--help` and `help --full` output.** This is
   generated from the Kong CLI struct in `internal/cli/drift/`. If it's
   wrong, fix the code — don't paper over it in the docs.
2. **`docs/drift-cli.md`** — an exhaustive reference. Every subcommand,
   every flag, copy-pasted from `drift <cmd> --help`. Humans read this when
   they need detail the README doesn't have.
3. **`README.md`** — the entry point. Keep it short. It needs a Commands
   section that matches `drift help` (the curated LLM-friendly top-level),
   a correct Quickstart, and a "Shorthand / cheat sheet" block near the
   top with the commands people actually type day to day.

If the three disagree, the binary wins.

## Workflow

### 1. Build and capture the current CLI surface

Go may not be on `$PATH` directly (this repo is developed on NixOS). Prefer
`go` if it resolves; otherwise fall back to the nix store path the user has:

```bash
GO=$(command -v go || ls /nix/store/*-go-*/bin/go 2>/dev/null | head -1)
```

Then run the bundled script, which shells out to `$GO run ./cmd/drift`:

```bash
bash .claude/skills/drift-docs/scripts/dump_cli.sh > /tmp/drift-cli.new.md
```

The script:
- runs `drift --help` for the top-level usage,
- runs `drift help --full` for the LLM-friendly catalog (including RPC
  methods and exit codes),
- walks every subcommand discovered from `--help` and captures
  `drift <cmd> --help` verbatim — including nested subcommands like
  `circuit set default`.

If the script fails to build, **stop**. A stale doc regenerated from a
broken binary is worse than no regen. Report the build error and ask the
user how they want to proceed.

### 2. Reconcile `docs/drift-cli.md`

Diff the fresh dump against the committed file and write it back:

```bash
diff -u docs/drift-cli.md /tmp/drift-cli.new.md || true
cp /tmp/drift-cli.new.md docs/drift-cli.md
```

If the diff is surprisingly large (e.g. whole sections missing that you'd
expect to be there), investigate before overwriting — a subcommand may
have been renamed or deleted and the user should know.

### 3. Reconcile `README.md`

Three specific sections to check:

**Commands** — should mirror `drift help` (the curated top-level, not the
full Kong dump). If a new top-level subcommand appeared, add it here; if
one was removed or renamed, update it. Keep the grouping (CIRCUITS /
KARTS / RUNS) consistent with what `drift help` prints.

**Quickstart** — every command in this block must run without error on a
fresh install. If `init` gained or lost a flag, or `new` changed shape,
update the example. Don't invent flags that don't exist.

**Shorthand / cheat sheet** — a compact block of the commands people type
daily. Keep it tight (≤15 lines). Example shape:

```text
drift list                   # karts + status
drift connect <kart>         # mosh/ssh in, auto-starts
drift new <kart> --clone <url> --character <id>
drift run ai                 # claude on the circuit, preloaded
drift status                 # circuits + lakitu health
```

It's a *cheat sheet*, not a manual — flags people won't remember anyway
don't belong here.

### 4. Sanity-check before declaring done

Run these and read the output:

```bash
$GO run ./cmd/drift --help
$GO run ./cmd/drift help --full
grep -E '^drift ' README.md
```

Then re-read the Commands section and the shorthand block with fresh eyes.
If a reader unfamiliar with drift couldn't go from README → first kart in
under five minutes, the Quickstart needs more work, not less.

### 5. Commit

Stage `README.md` and `docs/drift-cli.md` together — they're one logical
change. Commit message shape: `docs: sync README and drift-cli reference`
(or whatever the trigger was, e.g. `docs: refresh for new 'drift info' flag`).

Do **not** tag a release. The project's `CLAUDE.md` is explicit: tags
require the human to ask in the current turn.

## What not to do

- **Don't hand-write the subcommand reference.** The script exists so you
  can't drift (heh) from the binary. If the script is missing something
  (e.g. a new nested subcommand), fix the script.
- **Don't add "Coming soon" or forward-looking entries** to `docs/drift-cli.md`
  — this file is a mirror of what the binary does *today*.
- **Don't reference external repos or handles** beyond `kurisu-agent/drift`
  and its dependencies — see the project `CLAUDE.md`. Use placeholders in
  examples.
- **Don't touch CHANGELOG or release notes** unless the user asked for it.
  This skill is about reference docs, not release artifacts.

## Files this skill owns

- `README.md` (sections: Commands, Quickstart, Shorthand/cheat sheet)
- `docs/drift-cli.md` (created if missing)
- `.claude/skills/drift-docs/scripts/dump_cli.sh`

Everything else — architecture docs, design notes, changelog — is out of
scope unless the user specifically asks.
