# Kart-side deny-literals hook

## Problem

Claude Code instances inside karts run with no guardrail against leaking forbidden literals (real handles, real org/repo names, internal project names) into commits, PR bodies, gh comments, etc. Workstation-side, the host machine has a `PreToolUse` hook at `~/.claude/hooks/block-literals.sh` that scans tool inputs against a deny-list and refuses the call. That guardrail does NOT propagate into karts. Each kart has its own `~/.claude/` and would need its own hook installed by hand. The failure mode the workstation hook exists to prevent applies inside karts too: a Claude Code session running inside a kart can post forbidden handles to GitHub before the human notices.

## Goals

1. Every kart that opts into the `claudeCode` seed gets the same `PreToolUse` hook installed automatically, with a deny-list rendered from a chest-backed circuit-level config field.
2. Configuration lives in one place (circuit config), is referenced by chest so the literal list itself stays out of `~/.drift/garage/config.yaml` plaintext, and survives kart recreation without re-typing.
3. Empty / missing chest reference is a graceful no-op — the hook installs but lets every call through, so a circuit that has no privacy concerns still gets a benign drop-in.
4. The hook fires on Bash / Edit / Write / MultiEdit, blocking with a `permissionDecision: deny` JSON response that surfaces the matched pattern (not the surrounding text) to the model.

## Non-goals

- Per-character or per-tune deny-list overrides. Circuit-only for v1; layering comes later if anyone hits the wall.
- Regex matching. Plain fixed-string substring (`grep -F -i`) only. Regex deny-lists are footgunny and the literals we care about are well-defined.
- Hooks for tools other than the four named above.
- Mirroring the workstation host's deny-list file content automatically. The chest entry is independent — workstation and kart can hold different lists.
- A `drift deny-literals add/remove/list` CLI surface. For v1, the chest entry is edited directly via `lakitu chest set <name>` (paste the multi-line list on stdin).

## Configuration

**One new field on the server config (`~/.drift/garage/config.yaml`):**

```yaml
deny_literals: chest:my-deny-list
```

- `deny_literals` MUST be a `chest:<name>` reference (literal lists rejected at write time, mirroring how `pat_secret` is handled).
- The chest entry's value is the rendered deny-list file: one fixed-string pattern per line, `#` for comments, blank lines ignored. Verbatim what `~/.claude/deny-literals.txt` looks like on the workstation today.
- Empty / missing field skips the deny-list file entirely. The hook script still installs (cheap), and on each invocation it checks for the file and exits 0 silently when absent.

## Seed surface

One existing seed builtin gains new files: `claudeCode`. The `kartInfo` / `driftShell` seeds are untouched.

### `~/.claude/hooks/block-literals.sh` — constant

Verbatim copy of the workstation hook (`~/.claude/hooks/block-literals.sh`). Lives as a Go constant string in `internal/seed/builtins.go`. Always installed, regardless of whether `deny_literals` is configured. Mode `0755`.

### `~/.claude/deny-literals.txt` — conditional

Rendered from a new seed Var `{{ .DenyLiterals }}` whose value is the dechested content. The seed `Files` entry is gated on `{{ if .DenyLiterals }}` so karts on a circuit with no deny-list configured don't get a stray empty file. Mode `0600` so the kart user can read/edit it without exposing it to other UIDs in the container.

### `~/.claude/settings.json` — hooks block added

The existing `claudeSettingsJSON` constant gains a `hooks.PreToolUse` block:

```json
"hooks": {
  "PreToolUse": [
    {
      "matcher": "Bash|Edit|Write|MultiEdit",
      "hooks": [
        {
          "type": "command",
          "command": "$HOME/.claude/hooks/block-literals.sh"
        }
      ]
    }
  ]
}
```

Always emitted. The hook script's no-deny-list-equals-no-op behavior keeps this safe for circuits without a configured deny-list.

## Implementation

### Schema + dechest

- `internal/config/server.go` (or wherever `Server` lives) gains `DenyLiterals string` with the `chest:` prefix validation.
- `internal/kart.ServerDefaults` gains `DenyLiteralsChest string`. Threaded into `Resolver.Defaults` from the server handler at handler entry, exactly like `CircuitName`.
- `Resolver.Resolve` adds a step: if `Defaults.DenyLiteralsChest != ""` and `r.ResolveChestRef != nil`, dechest it; populate `Resolved.DenyLiterals`. Errors surface as `chest_entry_not_found`.

### Vars

- `kartVars` in `internal/kart/seed_fragment.go` adds:

  ```go
  "DenyLiterals": r.DenyLiterals,
  ```

  Empty string when nothing is configured. The seed template's `{{ if .DenyLiterals }}` guard handles the conditional drop.

### Seed builtins

- `internal/seed/builtins.go` gains:
  - A new constant `blockLiteralsHookScript` (verbatim hook content).
  - Two new entries in `claudeCode` `Files`:
    - `~/.claude/hooks/block-literals.sh` (constant content, mode `0755`, `BreakSymlinks: true`).
    - `~/.claude/deny-literals.txt` (templated content, mode `0600`, gated on `HasDenyLiterals`).
  - The existing `claudeSettingsJSON` constant gains the `hooks` block as static JSON. Always emitted.

The seed `File` schema may need a mode field if it does not already carry one. Check `internal/seed/seed.go`. If absent, add it.

### Tests

- Unit: `internal/seed` confirms the hook script lands when the seed is rendered, the deny-list file appears only when `DenyLiterals` is non-empty, and `settings.json` always carries the hook entry.
- Unit: `internal/kart` resolver test confirms a chest-backed `deny_literals` flows through to `Resolved.DenyLiterals`; missing chest entry surfaces `chest_entry_not_found`; missing field leaves `DenyLiterals` empty.
- Integration: extend `integration/gh_auth_test.go` (or new test) to verify a kart created on a circuit with `deny_literals: chest:...` configured ends up with `~/.claude/hooks/block-literals.sh` executable, `~/.claude/deny-literals.txt` containing the configured content, and `~/.claude/settings.json` carrying the `hooks` block.

## Dependencies inside the kart

The hook script needs `bash`, `jq`, and `grep` on PATH inside the kart. `bash` and `grep` are universal. `jq` is present in nearly every devcontainer-features-based image and in every `nixenv` tune output (drift-devtools bundles it). For minimal images that lack `jq`, the hook will fail noisily on every tool call, which is worse than no hook. We accept this for v1; if a real kart hits it, we either pin a `jq` install in the seed or rewrite the hook in pure shell.

## Migration

Pre-1.0, no migration. Add the field, push, recreate karts to pick up the new seed files. Existing karts keep running with no hook until they're recreated.

## Test plan

- `make ci` green.
- `make integration` green (with new test).
- Live on a circuit with a chest-backed deny-list:
  - Recreate a kart on `claudeCode`; confirm the three files land.
  - Inside the kart, run `claude code` and have it attempt a Bash command containing one of the deny literals — confirm the call is blocked with a `permissionDecision: deny` reason citing the matched pattern.
  - Confirm a clean Bash command passes through (hook is silent).
- Live on a circuit with no `deny_literals` configured:
  - Recreate a kart; confirm the hook script installs and `settings.json` carries the hook entry, but `~/.claude/deny-literals.txt` is absent and the hook silently no-ops.
