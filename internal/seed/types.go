// Package seed defines named bundles of files to drop into a freshly
// created kart's $HOME after `devpod up`. Templates are referenced by
// name from a tune's `seed:` list and resolve first against a built-in
// registry, then against `~/.drift/garage/seeds/<name>.yaml`.
//
// The package is deliberately declarative: a Template names files and
// their content, not shell. The kart finaliser converts a rendered
// template into a base64-pinned shell fragment via the existing
// container_script helpers — sandboxing the contract so user-supplied
// templates can't run arbitrary shell on the circuit.
package seed

// Template is one named seed bundle.
type Template struct {
	// Name is set by the loader, not the YAML — disk templates take
	// their name from the filename stem.
	Name string `yaml:"-"`

	// Files are the per-path drops applied in order. Order matters when
	// two files in the same template target the same path (last wins),
	// but in practice we don't expect duplicates.
	Files []File `yaml:"files"`
}

// ConflictMode is how seedFragments behaves when the destination
// already exists in the kart at write time. Default (zero value) is
// `overwrite` — the simplest semantics and matches the most common
// case where a seed is the source of truth for the file.
type ConflictMode string

const (
	// ConflictOverwrite replaces the destination unconditionally. Default.
	ConflictOverwrite ConflictMode = "overwrite"
	// ConflictSkip leaves the destination untouched if it exists. Useful
	// when a host bind-mount may already provide the file.
	ConflictSkip ConflictMode = "skip"
	// ConflictMerge deep-merges the seeded content into the existing
	// file (object/map keys union, arrays replaced, scalars patch-wins).
	// Only valid for `.json` / `.yaml` / `.yml` paths — checked at
	// render time. Read happens via one extra `devpod ssh` round trip;
	// the merge runs in lakitu, so containers need no extra tooling.
	ConflictMerge ConflictMode = "merge"
	// ConflictAppend writes the seeded content after the existing
	// content. Format-agnostic (just byte concatenation).
	ConflictAppend ConflictMode = "append"
	// ConflictPrepend writes the seeded content before the existing
	// content. Format-agnostic.
	ConflictPrepend ConflictMode = "prepend"
)

// File is one path/content pair inside a Template. `Path` and `Content`
// are both Go text/template strings rendered against the kart's Vars at
// finalise time.
type File struct {
	// Path is the destination inside the kart container. A leading `~/`
	// expands to $HOME at finalise time.
	Path string `yaml:"path"`

	// Content is the file body. Rendered as text/template against Vars.
	Content string `yaml:"content"`

	// OnConflict picks the strategy for handling an existing file at
	// the destination. Empty is treated as `overwrite`.
	OnConflict ConflictMode `yaml:"on_conflict,omitempty"`

	// BreakSymlinks, when true, removes a destination that is a symlink
	// before writing — so we end up with a real file in the kart rather
	// than overwriting through to the symlink target. CLAUDE.md needs
	// this when ~/.claude/ is bind-mounted from the host. Honoured for
	// every conflict mode that writes (everything except `skip`).
	BreakSymlinks bool `yaml:"break_symlinks,omitempty"`
}

// Vars is the template substitution context. Keys are stable kart-
// derived strings (Kart, Workspace, Image, DevcontainerPath); future
// CLI-passed vars layer on top of these via the same map. Values are
// strings so text/template's missingkey=zero substitutes "" for any
// undeclared key rather than the placeholder "<no value>".
type Vars map[string]string

// RenderedFile is the post-template form of a File: Path and Content
// both have their template directives substituted, and the flags carry
// through verbatim. The kart finaliser turns a slice of these into the
// shell fragment that runs inside the container.
type RenderedFile struct {
	Path          string
	Content       []byte
	OnConflict    ConflictMode
	BreakSymlinks bool
}
