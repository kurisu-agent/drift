# Codebase-wide simplify sweep

## Problem

The `/simplify` skill reviews a diff — great for in-flight work, useless for cruft that has already landed. Drift is ~20k LOC of Go plus ~2.6k LOC of integration tests, and nobody has done a holistic sweep for duplicated helpers, stringly-typed code, leaky abstractions, or dead branches since the initial CLI and server split. The point of this plan is to run the same three-lens review (reuse / quality / efficiency) over the whole codebase in parallel, report findings as structured markdown, and let a human decide what to fix.

## Goals

1. Cover every `.go` file under `cmd/`, `internal/`, and `integration/` exactly once.
2. Chunk into ~7 logical blocks of comparable size so parallel agents finish in similar wall-clock time.
3. Agents produce **findings only** — no code edits. Each chunk writes one markdown file under `plans/08-simplify-sweep/`. Fixes happen in a follow-up PR per chunk (or per cluster of related findings).
4. Findings use a consistent schema so they can be aggregated, triaged, and converted into fix tasks mechanically.

## Non-goals

- Running the sweep continuously — this is a one-shot. Subsequent sweeps are re-runs of this same plan.
- Touching generated code, vendored dependencies, or `.goreleaser.yaml` / `flake.nix` build plumbing.
- Making the agents fix what they find. Parallel fixes conflict; fixes land as a human-reviewed follow-up.

## Chunks

Chunks are sized by LOC (including tests) and organised by architectural seam rather than directory alphabetic. Each row is one parallel agent.

| # | Chunk | Paths | LOC | Focus |
|---|-------|-------|-----|-------|
| 1 | drift CLI | `cmd/drift/`, `internal/cli/drift/` | ~4620 | Command surface; argv parsing; user-facing output. Biggest chunk — consider splitting 1a (commands ≤130 LOC) and 1b (`circuit.go`, `migrate.go`, `update.go`, `new.go`, `run.go`) if it runs long. |
| 2 | Server runtime | `internal/server/` | ~3540 | Server-side RPC handlers, kart lifecycle, tune/verify/info endpoints. |
| 3 | Kart + config + data types | `internal/kart/`, `internal/config/`, `internal/chest/`, `internal/name/`, `internal/model/`, `internal/run/` | ~3945 | Domain model, on-disk layout, config I/O. Watch for string-typed enums and duplicate validators. |
| 4 | Devpod + connect + warmup | `internal/devpod/`, `internal/connect/`, `internal/sshconf/`, `internal/systemd/`, `internal/warmup/` | ~4145 | External-process orchestration and SSH plumbing. Heavy on shell-outs — prime territory for duplicated path handling. |
| 5 | Transport + utilities | `internal/rpc/`, `internal/rpcerr/`, `internal/wire/`, `internal/exec/`, `internal/slogfmt/`, `internal/version/` | ~2860 | RPC wire format, exec shim (termux), log formatting. |
| 6 | lakitu + shared CLI infra | `cmd/lakitu/`, `internal/cli/lakitu/`, `internal/cli/{errfmt,progress,style}/`, `internal/clihelp/`, `internal/cliscript/` | ~1665 | Server-side CLI + shared TUI/help helpers used by both clients. |
| 7 | Integration tests | `integration/` | ~2635 | Test harness and scenarios. Watch for duplicated setup and oversized `harness.go`. |

Chunks do not overlap. Any cross-chunk finding (e.g. "this helper in chunk 5 could replace the inline code in chunks 1 and 2") must still land in exactly one findings file — whichever chunk holds the *duplicate* is the caller — with a note to cross-reference.

## Findings schema

Each agent writes `plans/08-simplify-sweep/<chunk-slug>.md` with this structure:

```markdown
# Simplify sweep — <chunk name>

**Paths reviewed:** <comma-separated list>
**Agent:** <model/notes>

## Summary

<3-5 bullet headline findings, ordered by payoff>

## Findings

### F1. <short title> — <severity: low|med|high>

- **Where:** `<file>:<line-range>`
- **What:** <1-2 sentences>
- **Why it matters:** <user-visible impact or risk>
- **Suggested fix:** <concrete — point at the helper to reuse, the type to introduce, the dead branch to delete>
- **Cross-ref:** <optional: other chunks affected>

<repeat per finding, numbered F1..Fn>

## Nothing to flag

<list any sub-areas the agent explicitly cleared, so re-runs can skip them>
```

Severity guidance:
- **high** — actual bug, security issue, or clear duplicated logic across files
- **med** — obvious cleanup that pays back (leaky abstraction, stringly-typed, redundant state)
- **low** — nitpicks and stylistic polish; agents should batch these rather than listing 30

## Agent contract

Every agent receives the same review rubric (reuse / quality / efficiency, mirroring `/simplify`), plus:

1. **Read-only.** No `Edit`/`Write` against source files. Only write the findings markdown for your chunk.
2. **Ground every finding in a concrete file+line.** No "there might be duplication somewhere in this package" — point at the line.
3. **Check before flagging duplication.** Before claiming "this could reuse helper X", confirm helper X actually exists and has a compatible signature — grep for it.
4. **Skip generated files.** `wire_gen.go` (if any), `*.pb.go`, etc.
5. **Cap low-severity findings at 10.** If you have 40 nits, pick the 10 highest-leverage and note the rest as a one-line "lots of minor X across this package".
6. **Report back under 200 words in the tool result.** The full report is in the markdown file — the tool result is a pointer + count of findings by severity.

## Runbook

1. Create `plans/08-simplify-sweep/` (agents will write their findings files into it).
2. Spawn 7 `general-purpose` agents in parallel, one per chunk. Each gets the chunk paths, the rubric, and the findings schema above.
3. Aggregate: once all agents finish, skim the 7 findings files, deduplicate cross-chunk duplicates, and produce a one-page triage at `plans/08-simplify-sweep/TRIAGE.md` that groups findings into (a) fix now, (b) fix later, (c) won't fix.
4. Convert (a) into feature-branch PRs — one per cluster of related findings, not one giant PR.

## Follow-up

A sweep is only useful if it becomes a habit. After the first run, decide whether to:

- Re-run on a schedule (e.g. quarterly) via the same plan — delete the findings dir, re-spawn.
- Fold the highest-yield checks into `.golangci.yml` so they catch regressions in CI without needing agents.
- Extend the rubric with drift-specific patterns (e.g. "never call `os.Executable` without the termux fallback") once we see which gotchas recur.
