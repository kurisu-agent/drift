package seed

// claudeCodeMD is the orientation blurb dropped at $HOME/.claude/CLAUDE.md
// inside karts that opt into the `claudeCode` seed. The pointer to the
// devcontainer config gives the in-container Claude Code session a clear
// anchor for "what kind of environment am I in".
const claudeCodeMD = "You are inside a devcontainer{{ if .Image }} running `{{ .Image }}`{{ end }}. Config: @{{ .DevcontainerPath }}\n"

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
