// Package model holds domain types shared across drift's internal
// packages that would otherwise duplicate to break dep cycles.
package model

// Tune is the on-disk shape for garage/tunes/<name>.yaml. Used by both
// the server handlers (which read/write YAML) and the kart resolver
// (which composes defaults at kart.new time).
type Tune struct {
	Starter      string  `yaml:"starter,omitempty" json:"starter,omitempty"`
	Devcontainer string  `yaml:"devcontainer,omitempty" json:"devcontainer,omitempty"`
	DotfilesRepo string  `yaml:"dotfiles_repo,omitempty" json:"dotfiles_repo,omitempty"`
	Features     string  `yaml:"features,omitempty" json:"features,omitempty"`
	Env          TuneEnv `yaml:"env,omitempty" json:"env,omitempty"`
	// MountDirs becomes a `mounts` overlay on the devcontainer.json
	// delivered via --extra-devcontainer-path. devpod's merge unions
	// these with the project's mounts, deduped by target, so the
	// project repo never needs to be edited to add host binds.
	MountDirs []Mount `yaml:"mount_dirs,omitempty" json:"mount_dirs,omitempty"`
	// Seed names a list of seed templates to apply post-`devpod up`. Each
	// name resolves first against the built-in registry (e.g. `claudeCode`)
	// and then against `~/.drift/garage/seeds/<name>.yaml`. A seed template
	// is a declarative bundle of files to drop into the kart's $HOME with
	// optional Go-template substitution from a fixed set of kart-derived
	// vars. See internal/seed for the schema.
	Seed []string `yaml:"seed,omitempty" json:"seed,omitempty"`
}

// Mount mirrors the devcontainer.json mount shape (and the skevetter/devpod
// fork's pkg/devcontainer/config.Mount). Kept in model/ so server, kart, and
// CLI packages can share one definition without dep cycles.
type Mount struct {
	Type     string   `yaml:"type,omitempty" json:"type,omitempty"`
	Source   string   `yaml:"source,omitempty" json:"source,omitempty"`
	Target   string   `yaml:"target,omitempty" json:"target,omitempty"`
	External bool     `yaml:"external,omitempty" json:"external,omitempty"`
	Other    []string `yaml:"other,omitempty" json:"other,omitempty"`
}

// MountTypeCopy is a lakitu-only pseudo-mount type. Entries with this
// type are dropped into the container as file copies post-`devpod up`
// rather than forwarded to docker as real mounts.
const MountTypeCopy = "copy"

// TuneEnv groups chest-backed env vars by the injection site that
// consumes them. Every value must be a chest:<name> reference. A key
// may repeat across blocks; each block is independent with no cross-
// block precedence.
type TuneEnv struct {
	// Build is exposed to the dotfiles install script(s) during
	// kart.new. Reaches devpod's --dotfiles install via
	// `devpod up --dotfiles-script-env` and drift's own layer-1
	// install via process-env on the install-dotfiles invocation.
	// Scoped to those processes only; never lands in the container's
	// containerEnv and is gone once provisioning completes.
	Build map[string]string `yaml:"build,omitempty" json:"build,omitempty"`

	// Workspace is passed to `devpod up --workspace-env` and becomes part
	// of the container env for the workspace's lifetime; re-applied on
	// kart.start / kart.restart. Visible via /proc/<pid>/environ and
	// `docker inspect`.
	Workspace map[string]string `yaml:"workspace,omitempty" json:"workspace,omitempty"`

	// Session is passed to `devpod ssh --set-env` each time the user
	// opens a shell via drift connect / drift ssh. Scoped to the ssh
	// channel only.
	Session map[string]string `yaml:"session,omitempty" json:"session,omitempty"`
}

// IsEmpty reports whether no env vars are configured across any block.
func (e TuneEnv) IsEmpty() bool {
	return len(e.Build) == 0 && len(e.Workspace) == 0 && len(e.Session) == 0
}

// KartSource is the source sub-object of kart.info. Shared because the
// kart-creation Result and the server-side info response both carry it.
type KartSource struct {
	Mode string `json:"mode"`
	URL  string `json:"url,omitempty"`
}

// MigratedFrom identifies the devpod workspace a kart was originally
// adopted from via `drift migrate`. Persisted on the kart config so
// subsequent migrate runs can filter out already-migrated workspaces
// even when the kart has been renamed. Absent on non-migrated karts.
type MigratedFrom struct {
	Context string `yaml:"context" json:"context"`
	Name    string `yaml:"name" json:"name"`
}

// IsZero reports whether no migration origin was recorded.
func (m MigratedFrom) IsZero() bool { return m.Context == "" && m.Name == "" }

// SourceMode is the closed enum for a kart's creation source. Persisted as
// the raw string (YAML tag keeps omitempty compatibility with existing
// on-disk configs); the Go type only affects compile-time callers.
type SourceMode string

const (
	// SourceModeClone: kart provisioned from an existing git repo.
	SourceModeClone SourceMode = "clone"
	// SourceModeStarter: kart seeded from a starter template.
	SourceModeStarter SourceMode = "starter"
	// SourceModeNone: kart created with no source (blank workspace).
	SourceModeNone SourceMode = "none"
)

// KartConfig is the unified on-disk shape for garage/karts/<name>/config.yaml.
// Used by both the kart.new writer and the server reader so the two sides
// cannot drift silently. Additive only — every field is omitempty so older
// on-disk files still round-trip.
type KartConfig struct {
	Repo         string        `yaml:"repo,omitempty" json:"repo,omitempty"`
	Tune         string        `yaml:"tune,omitempty" json:"tune,omitempty"`
	Character    string        `yaml:"character,omitempty" json:"character,omitempty"`
	SourceMode   string        `yaml:"source_mode,omitempty" json:"source_mode,omitempty"`
	User         string        `yaml:"user,omitempty" json:"user,omitempty"`
	Shell        string        `yaml:"shell,omitempty" json:"shell,omitempty"`
	Image        string        `yaml:"image,omitempty" json:"image,omitempty"`
	Workdir      string        `yaml:"workdir,omitempty" json:"workdir,omitempty"`
	CreatedAt    string        `yaml:"created_at,omitempty" json:"created_at,omitempty"`
	Autostart    bool          `yaml:"autostart,omitempty" json:"autostart,omitempty"`
	Env          TuneEnv       `yaml:"env,omitempty" json:"env,omitempty"`
	MountDirs    []Mount       `yaml:"mount_dirs,omitempty" json:"mount_dirs,omitempty"`
	MigratedFrom *MigratedFrom `yaml:"migrated_from,omitempty" json:"migrated_from,omitempty"`
}

// Character is the on-disk shape for garage/characters/<name>.yaml. PATSecret
// always carries a chest:<name> reference — literal tokens are rejected at
// add time. The resolved (dechested) form lives elsewhere as a separate
// type so the boundary between "on disk" and "resolved" stays explicit.
type Character struct {
	GitName    string `yaml:"git_name" json:"git_name"`
	GitEmail   string `yaml:"git_email" json:"git_email"`
	GithubUser string `yaml:"github_user,omitempty" json:"github_user,omitempty"`
	SSHKeyPath string `yaml:"ssh_key_path,omitempty" json:"ssh_key_path,omitempty"`
	PATSecret  string `yaml:"pat_secret,omitempty" json:"pat_secret,omitempty"`
}
