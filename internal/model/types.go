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
}

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
