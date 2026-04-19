// Package model holds domain types shared across drift's internal
// packages that would otherwise duplicate to break dep cycles.
package model

// Tune is the on-disk shape for garage/tunes/<name>.yaml. Used by both
// the server handlers (which read/write YAML) and the kart resolver
// (which composes defaults at kart.new time).
type Tune struct {
	Starter      string `yaml:"starter,omitempty" json:"starter,omitempty"`
	Devcontainer string `yaml:"devcontainer,omitempty" json:"devcontainer,omitempty"`
	DotfilesRepo string `yaml:"dotfiles_repo,omitempty" json:"dotfiles_repo,omitempty"`
	Features     string `yaml:"features,omitempty" json:"features,omitempty"`
}

// KartSource is the source sub-object of kart.info. Shared because the
// kart-creation Result and the server-side info response both carry it.
type KartSource struct {
	Mode string `json:"mode"`
	URL  string `json:"url,omitempty"`
}
