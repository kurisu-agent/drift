package kart

import (
	"strings"
)

// ghAuthFragment emits the post-up shell that wires per-kart identity
// and runtime auth from the resolved character. Two independent blocks:
//
//  1. Identity. `git config --global user.name / user.email /
//     github.user` runs whenever the character defines those fields,
//     PAT or no PAT. Devpod's agent writes a `[user]` block in the
//     kart's home gitconfig at workspace setup carrying the lakitu
//     host's identity (the SSH_INJECT_GIT_CREDENTIALS=false context
//     option only suppresses the SSH-time credential helper, not the
//     init-time copy in devpod 0.22.0). This block overwrites that
//     leak with the character's identity. PAT-less characters that
//     just want to be a named author (commits, no GitHub auth) need
//     this block; an earlier version gated the whole fragment on PAT
//     and PAT-less karts inherited the host identity.
//  2. gh auth login. Runs only when char.PAT != "". Logs gh in with
//     `gh auth login --with-token` and runs `gh auth setup-git` so
//     `git push` / `git pull` flow through gh's credential helper.
//
// Secrets travel via stdin (containerScript.Run pipes the assembled
// script to `bash -s`), so the PAT never lands on lakitu's argv table
// or the kart's container argv.
//
// gh CLI is required for the PAT branch — the github-cli devcontainer
// feature is wired by injectGithubCLIFeature whenever a character
// carries a PAT, so by the time this fragment runs `gh` is on
// /usr/local/bin. The else-branch is defensive: if a tune has somehow
// suppressed the feature, the fragment surfaces a warning rather than
// silently failing the post-up SSH session under `set -e`.
func ghAuthFragment(char *Character) string {
	if char == nil {
		return ""
	}
	var b strings.Builder
	if char.GitName != "" {
		b.WriteString("git config --global user.name " + shellSingleQuote(char.GitName) + "\n")
	}
	if char.GitEmail != "" {
		b.WriteString("git config --global user.email " + shellSingleQuote(char.GitEmail) + "\n")
	}
	if char.GithubUser != "" {
		b.WriteString("git config --global github.user " + shellSingleQuote(char.GithubUser) + "\n")
	}
	if char.PAT != "" {
		b.WriteString("if command -v gh >/dev/null 2>&1; then\n")
		b.WriteString(`  printf '%s\n' ` + shellSingleQuote(char.PAT) + " | gh auth login --with-token --hostname github.com\n")
		b.WriteString("  gh auth setup-git --hostname github.com\n")
		b.WriteString("else\n")
		b.WriteString(`  echo "gh-auth: gh CLI not on PATH; private github operations will fail until gh is installed" >&2` + "\n")
		b.WriteString("fi\n")
	}
	return b.String()
}

// shellSingleQuote wraps s in single quotes for safe inclusion in a
// posix-shell script. Single-quoted strings interpret nothing except
// the closing quote, so the only character that needs escaping is `'`
// itself; the canonical workaround is to close the quoted span, emit
// an escaped quote, and re-open: `'\”`. Everything else (`$`, `\`,
// backticks, newlines) passes through verbatim.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
