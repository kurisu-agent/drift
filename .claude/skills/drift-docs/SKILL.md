---
name: drift-docs description: Refresh drift's user-facing docs so they match the actual CLI and RPC surface. Regenerates `docs/drift-cli.md` as an exhaustive per-subcommand `--help` reference, regenerates `docs/lakitu-rpc.md` as the JSON-RPC protocol + method catalog, verifies the hand-curated `drift help` in `internal/cli/drift/help.go` still covers every leaf command, and keeps the README's Commands/Quickstart/Shorthand sections in sync. Invoke whenever the user says "update the docs", "sync the README", "regenerate the CLI reference", "docs are stale", "refresh the RPC reference", after CLI flags or subcommands change, after adding or renaming a wire method, or when preparing a release. Prefer this skill over ad-hoc editing — it's what keeps the docs, help.go, and the binaries from drifting apart.
---

# drift-docs

The `drift` binary, the `lakitu` binary, and `internal/wire/methods.go` are the source of truth. Docs exist to serve humans reading them; they're only useful if they match what the code actually does. This skill's job is to close that gap across three artifacts:

- `docs/drift-cli.md` — exhaustive per-subcommand `--help` dump
- `docs/lakitu-rpc.md` — JSON-RPC protocol + method catalog
- `README.md` — Commands, Quickstart, and shorthand cheat-sheet

Plus one correctness check on the code itself:

- `internal/cli/drift/help.go`'s `driftHelpSections` — the hand-curated `drift help` output. Unlike `drift help --full` (auto-derived, cannot drift), this block is edited by hand and can go stale.

If any of these disagree with the binary, the binary wins.

## Workflow

Go may not be on `$PATH` directly (this repo is developed on NixOS). The bundled scripts locate `go` automatically, falling back to `/nix/store`. You can still set `GO=<path>` explicitly if needed.

### 1. Regenerate `docs/drift-cli.md`

```bash
bash .claude/skills/drift-docs/scripts/dump_cli.sh > /tmp/drift-cli.new.md
diff -u docs/drift-cli.md /tmp/drift-cli.new.md || true
cp /tmp/drift-cli.new.md docs/drift-cli.md
```

The script walks the Kong command tree (including nested leaves like `circuit set default`), so new subcommands appear automatically. If the diff is surprisingly large — whole sections missing — investigate before overwriting. A subcommand may have been renamed or deleted and the user should know before it disappears from the committed reference.

### 2. Regenerate `docs/lakitu-rpc.md`

```bash
bash .claude/skills/drift-docs/scripts/dump_rpc.sh > /tmp/lakitu-rpc.new.md
diff -u docs/lakitu-rpc.md /tmp/lakitu-rpc.new.md 2>/dev/null || true
cp /tmp/lakitu-rpc.new.md docs/lakitu-rpc.md
```

The script sources the method list from `lakitu help` (which renders `wire.Methods()`), the error-type taxonomy from `internal/rpcerr/rpcerr.go`, and a short hand-written transport/versioning preamble. If you added a wire method, confirm it shows up in the methods block — if it doesn't, you forgot to register it in `internal/wire/methods.go` (the one source both sides read from).

### 3. Verify `internal/cli/drift/help.go` is still accurate

```bash
bash .claude/skills/drift-docs/scripts/verify_help.sh
```

The script prints both the auto-derived leaf catalog (`drift help --full`) and the hand-curated output (`drift help`), then checks that every leaf path has its tokens represented somewhere in the curated sections. If a leaf is missing, the script exits non-zero and names it.

Token-coverage is coarse — it'll miss **stale** rows (a curated line pointing at a removed command, or using an old name). Read the curated output by eye and confirm:

- Every row still names a real subcommand.
- Groupings (CIRCUITS / KARTS / RUNS) still match the command's role.
- Shorthand like `circuit add|list|rm` lists the actual present leaves.

Edit `driftHelpSections` in `internal/cli/drift/help.go` to fix — the same file ships a comment reminding future-you that `--full` is the safety net. When the set of top-level verbs changes, `driftHelpFullIntro` may also want a tweak.

lakitu's equivalent (`internal/cli/lakitu/help.go`) is purely auto-derived through `clihelp.Render` and has no curated block to maintain. A passing `lakitu help` run is enough.

### 4. Reconcile `README.md`

Three specific sections to check:

**Commands** — should mirror `drift help` (the curated top-level, not the full Kong dump). If a new top-level subcommand appeared, add it here; if one was removed or renamed, update it. Keep the grouping (CIRCUITS / KARTS) consistent with what `drift help` prints.

**Quickstart** — every command in this block must run without error on a fresh install. If `init` gained or lost a flag, or `new` changed shape, update the example. Don't invent flags that don't exist.

**Shorthand / cheat sheet** — a compact block of the commands people type daily. Keep it tight (≤15 lines). Example shape:

```text
drift connect                # cross-circuit picker (TTY only)
drift connect <kart>         # mosh/ssh in, auto-starts
drift connect -l             # list karts on the target circuit
drift new <kart> --clone <url> --character <id>
drift status                 # circuits + lakitu health + per-circuit karts
drift circuit                # list circuits (same as `drift circuit list`)
```

It's a *cheat sheet*, not a manual — flags people won't remember anyway don't belong here. If the README already has a Commands block that doubles as shorthand, consolidate rather than duplicate.

### 5. Sanity-check before declaring done

```bash
GO=$(command -v go || ls /nix/store/*-go-*/bin/go 2>/dev/null | head -1)
$GO run ./cmd/drift --help
$GO run ./cmd/drift help
$GO run ./cmd/drift help --full
$GO run ./cmd/lakitu help
```

Then re-read the Commands section and the shorthand block with fresh eyes. If a reader unfamiliar with drift couldn't go from README → first kart in under five minutes, the Quickstart needs more work, not less.

### 6. Commit

Stage `README.md`, `docs/drift-cli.md`, `docs/lakitu-rpc.md`, and any `internal/cli/drift/help.go` edits together — they're one logical change. Commit message shape: `docs: sync README + CLI/RPC references` (or something more specific, e.g. `docs,help: wire up new 'drift foo' flag`).

Do **not** tag a release. The project's `CLAUDE.md` is explicit: tags require the human to ask in the current turn.

## What not to do

- **Don't hand-write the subcommand reference or RPC method list.** The scripts exist so the docs can't drift from the binaries. If a script is missing something (e.g. a new nested subcommand, a new wire method), fix the script.
- **Don't add "Coming soon" or forward-looking entries** to `docs/drift-cli.md` or `docs/lakitu-rpc.md` — they're mirrors of what the binaries do *today*.
- **Don't paper over a bad help message in the docs.** If `drift foo --help` is wrong, the fix is in the Kong struct (`internal/cli/drift/foo.go`) or its `help:"…"` tag, not in the generated markdown.
- **Don't reference external repos or handles** beyond `kurisu-agent/drift` and its dependencies — see the project `CLAUDE.md`. Use placeholders in examples.
- **Don't touch CHANGELOG or release notes** unless the user asked for it. This skill is about reference docs, not release artifacts.

## Files this skill owns

- `README.md` (sections: Commands, Quickstart, Shorthand/cheat sheet)
- `docs/drift-cli.md` (created if missing)
- `docs/lakitu-rpc.md` (created if missing)
- `.claude/skills/drift-docs/scripts/dump_cli.sh`
- `.claude/skills/drift-docs/scripts/dump_rpc.sh`
- `.claude/skills/drift-docs/scripts/verify_help.sh`

## Files this skill inspects (and may propose edits to)

- `internal/cli/drift/help.go` — curated `drift help` output; edit `driftHelpSections` and `driftHelpFullIntro` when the top-level verb set changes.

Everything else — architecture docs, design notes, changelog — is out of scope unless the user specifically asks.
