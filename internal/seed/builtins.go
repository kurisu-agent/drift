package seed

// claudeCodeMD is the orientation blurb dropped at $HOME/.claude/CLAUDE.md
// inside karts that opt into the `claudeCode` seed. Includes the kart
// identity stamp so an in-kart `claude` agent knows which kart /
// character / circuit it's running as without having to read info.json
// on every prompt. Agents are expected to extend this file with
// project-specific notes below the stamp; the stamp itself is
// regenerated each kart.new and shouldn't be hand-edited.
//
// The trailing claudeGHAuthBlock is gated on {{ if .HasPAT }} so the
// stamp only claims gh auth on karts where drift actually wired it
// (resolved character or kart pat slug yielded a PAT).
const claudeCodeMD = `# kart ` + "`{{ .Kart }}`" + `{{ if .Character }} ({{ .Character }}){{ end }} on circuit ` + "`{{ .Circuit }}`" + `

You are inside a devcontainer{{ if .Image }} running ` + "`{{ .Image }}`" + `{{ end }}. Devcontainer config: @{{ .DevcontainerPath }}. Canonical identity: @~/.drift/info.json (regenerated each ` + "`kart.new`" + `; don't edit).
` + claudeGHAuthBlock + `
Agents may append project-specific notes below this stamp.
`

// claudeGHAuthBlock is the gh-auth advisory appended to claudeCodeMD.
// Lives in its own constant so wording stays in sync as the gh-auth
// flow evolves. Kept blank when no PAT is wired so the stamp doesn't
// promise auth that doesn't exist.
const claudeGHAuthBlock = `{{ if .HasPAT }}
This kart is authenticated with the GitHub CLI (` + "`gh`" + `) using a personal access token scoped to {{ if .Character }}character ` + "`{{ .Character }}`" + `{{ else }}this kart{{ end }}. Use ` + "`gh`" + ` for both git operations and the GitHub API:

- ` + "`gh pr create`" + `, ` + "`gh pr merge`" + `, ` + "`gh issue list`" + `, ` + "`gh workflow run`" + ` for API-driven workflows.
- ` + "`git push`" + ` and ` + "`git pull`" + ` work transparently — gh's credential helper supplies the token to git.
- Prefer ` + "`gh api`" + ` over hand-rolled ` + "`curl https://api.github.com/...`" + ` calls; gh handles auth, pagination, and rate limits.

Don't write new credentials yourself (no ` + "`~/.git-credentials`" + `, no PATs in shell history). The kart's token is set when the kart is created; rotate by recreating the kart against an updated PAT registry entry.
{{ end }}`

// claudeCodeJSON is the minimal ~/.claude.json that suppresses Claude
// Code's first-run "Select login method" screen and the per-folder "Do
// you trust this folder?" prompt for the kart's workspace path. Keys
// discovered empirically; Claude Code does not document them but they
// are stable across recent releases. Deep-merged into whatever is
// already at the destination (a stub left by claude's own first-run
// during devtools-feature install bakes installMethod/userID into the
// image layer; we layer hasCompletedOnboarding + the trust map on
// top, which is what actually disables the picker).
const claudeCodeJSON = `{
  "hasCompletedOnboarding": true,
  "projects": {
    "{{ .Workspace }}": {
      "hasTrustDialogAccepted": true,
      "hasCompletedProjectOnboarding": true
    }
  }
}
`

// kartInfoJSON is the static display surface for in-kart UI consumers
// (zellij topbar, oh-my-posh prompt, nix-env-claude-status). Rendered at
// kart.new from the resolver's view of the world and dropped at
// $HOME/.drift/info.json — a single small well-defined schema, vs
// mounting the raw garage YAML files (which would expose host paths
// and PII the kart's UI doesn't need). Edits to character / tune YAML
// on the host don't propagate into a running kart; recreate the kart
// to refresh.
//
// Nested layout: each entity (kart, character, circuit) gets its own
// object so the same field name (`name`, `icon`, `color`) is unambiguous
// across them. `character.icon` is rendered (catalog name resolved to
// a glyph by lakitu), `kart.icon` likewise; flake consumers can `jq` a
// single path without translating.
const kartInfoJSON = `{
  "kart": {
    "name": "{{ .Kart }}",
    "icon": "{{ .Icon }}",
    "color": "{{ .Color }}"
  },
  "character": {
    "name": "{{ .Character }}",
    "display_name": "{{ .CharacterDisplayName }}",
    "icon": "{{ .CharacterIcon }}",
    "color": "{{ .CharacterColor }}"
  },
  "circuit": {
    "name": "{{ .Circuit }}"
  },
  "timezone": "{{ .Timezone }}"
}
`

// claudeSettingsJSON is drift's opinionated kart-side ~/.claude/settings.json.
// Owns the file outright (overwrite, not merge) — the seed system's
// merge runs against pre-copy state today and would silently drop
// host-mount-copy keys, plus a host-mounted settings.json from a
// Nix-managed workstation typically pins an absolute /nix/store/...
// path that doesn't exist in the kart and breaks claude's statusbar
// entirely. Drift therefore writes the kart-appropriate file directly:
//   - statusLine.command → bare "nix-env-claude-status" so it resolves
//     through PATH to the binary the nixenv flake ships.
//   - skipDangerousModePermissionPrompt → true so `yolo` doesn't hit
//     the bypass-permissions consent dialog every kart.
//   - effortLevel = "xhigh" + alwaysThinkingEnabled = true +
//     CLAUDE_CODE_DISABLE_ADAPTIVE_THINKING = "1" → maximum-effort
//     thinking that actually persists across sessions. As of April
//     2026 the literal "max" value isn't accepted by the
//     settings.json schema (see anthropics/claude-code#43322,
//     anthropics/claude-code#33937); xhigh is the top setting that
//     persists, the env var locks off adaptive downshifts.
//   - CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS = "1" → enables agent
//     teams.
//   - hooks.PreToolUse → always wired to the bundled
//     block-literals.sh (see plan 20). Empty deny-list = no-op, so
//     the entry is safe to ship unconditionally.
const claudeSettingsJSON = `{
  "skipDangerousModePermissionPrompt": true,
  "effortLevel": "xhigh",
  "alwaysThinkingEnabled": true,
  "env": {
    "CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS": "1",
    "CLAUDE_CODE_DISABLE_ADAPTIVE_THINKING": "1"
  },
  "statusLine": {
    "type": "command",
    "command": "nix-env-claude-status",
    "padding": 0
  },
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash|Edit|Write|MultiEdit",
        "hooks": [
          {
            "type": "command",
            "command": "bash $HOME/.claude/hooks/block-literals.sh"
          }
        ]
      }
    ]
  }
}
`

// driftShellBashrcStub is appended to a fresh kart's ~/.bashrc and is
// the only ~/.bashrc-owned line drift writes. The actual exec logic
// (auto-attach zellij on SSH, fall back to zsh otherwise) lives in the
// nixenv flake at $HOME/.nix-profile/share/nix-env/bashrc-bootstrap —
// iterating on that behaviour is a flake bump rather than a lakitu
// rebuild. The `[ -f … ] &&` guard makes the stub a no-op when the
// flake isn't installed, so a tune that lists `driftShell` without the
// Nix devcontainer feature degrades cleanly to plain bash.
//
// zsh is delivered with ZDOTDIR pre-set by the flake's wrapper, so
// there is no ~/.zshrc seed file at all; zsh sources its rc straight
// from the Nix store.
const driftShellBashrcStub = `
# --- drift kart shell bootstrap ---
[ -f "$HOME/.nix-profile/share/nix-env/bashrc-bootstrap" ] && source "$HOME/.nix-profile/share/nix-env/bashrc-bootstrap"
`

// blockLiteralsHookScript is the bash body of the kart-side PreToolUse
// hook installed at ~/.claude/hooks/block-literals.sh by the
// `claudeCode` seed. Mirrors the workstation hook at the same path.
// Reads the deny-list from ~/.claude/deny-literals.txt (one
// fixed-string pattern per line, `#` for comments, blank lines
// ignored), case-insensitive substring match against the tool input,
// and surfaces a `permissionDecision: deny` reason that names the
// matched pattern (not the surrounding text).
//
// Empty / missing deny-list = silent no-op so a circuit with no
// `deny_literals` configured still gets the script installed
// harmlessly. Depends on bash + jq + grep; all three are present in
// every standard devcontainer image and in nixenv tunes via
// drift-devtools. See plan 20.
const blockLiteralsHookScript = `#!/usr/bin/env bash
# Kart-side PreToolUse hook: block any Bash / Edit / Write / MultiEdit
# tool call whose payload contains a literal listed in
# ~/.claude/deny-literals.txt (one fixed-string pattern per line; '#'
# starts a comment; blank lines ignored). Matching is case-insensitive
# (grep -F -i). Empty / missing deny-list = silent no-op. Installed by
# the claudeCode seed; see plan 20.
set -euo pipefail

DENY_FILE="${CLAUDE_DENY_LITERALS:-$HOME/.claude/deny-literals.txt}"

if [ ! -r "$DENY_FILE" ]; then
  exit 0
fi

input=$(cat)

text=$(jq -r '
  [
    .tool_input.command       // empty,
    .tool_input.new_string    // empty,
    .tool_input.old_string    // empty,
    .tool_input.content       // empty,
    (.tool_input.edits // [] | map(.old_string, .new_string) | join("\n"))
  ] | join("\n")
' <<<"$input")

[ -n "$text" ] || exit 0

mapfile -t patterns < <(grep -vE '^\s*(#|$)' "$DENY_FILE" || true)
[ "${#patterns[@]}" -gt 0 ] || exit 0

matched=""
for p in "${patterns[@]}"; do
  if grep -F -i -q -- "$p" <<<"$text"; then
    matched+="  - $p"$'\n'
  fi
done

if [ -n "$matched" ]; then
  reason=$(printf 'Tool call blocked by drift kart deny-literals (%s):\n%sRewrite using generic placeholders. The list is sourced from the circuit-side chest entry referenced by deny_literals in the server config.' \
    "$DENY_FILE" "$matched")
  jq -n --arg r "$reason" '{
    hookSpecificOutput: {
      hookEventName: "PreToolUse",
      permissionDecision: "deny",
      permissionDecisionReason: $r
    }
  }'
fi

exit 0
`

// builtins is the in-process registry consulted before the on-disk
// garage/seeds/ directory. Keys are the canonical names that appear in
// a tune's `seed:` list.
var builtins = map[string]Template{
	"claudeCode": {
		Name: "claudeCode",
		Files: []File{
			{
				Path:          "~/.claude/CLAUDE.md",
				Content:       claudeCodeMD,
				BreakSymlinks: true,
			},
			{
				Path:       "~/.claude.json",
				Content:    claudeCodeJSON,
				OnConflict: ConflictMerge,
			},
			{
				Path:    "~/.claude/settings.json",
				Content: claudeSettingsJSON,
				// ConflictOverwrite (default): drift owns kart-side settings.
			},
			{
				// Hook script always installs; the settings.json above
				// always wires it. Plan 20.
				Path:          "~/.claude/hooks/block-literals.sh",
				Content:       blockLiteralsHookScript,
				BreakSymlinks: true,
			},
			{
				// Deny-list content. {{ .DenyLiterals }} is empty when
				// the circuit has no chest-backed list configured;
				// the hook script treats an empty file as a silent
				// no-op (mapfile reads zero patterns, exit 0).
				Path:          "~/.claude/deny-literals.txt",
				Content:       "{{ .DenyLiterals }}",
				BreakSymlinks: true,
			},
		},
	},
	"driftShell": {
		Name: "driftShell",
		Files: []File{
			{
				Path:       "~/.bashrc",
				Content:    driftShellBashrcStub,
				OnConflict: ConflictAppend,
			},
		},
	},
	"kartInfo": {
		Name: "kartInfo",
		Files: []File{
			{
				Path:    "~/.drift/info.json",
				Content: kartInfoJSON,
			},
		},
	},
}

// IsBuiltin reports whether a name is served by the in-process registry.
// Loaders use this to short-circuit the disk lookup and to surface a
// "name shadowed by built-in" diagnostic if a user authors a same-named
// disk template.
func IsBuiltin(name string) bool {
	_, ok := builtins[name]
	return ok
}
