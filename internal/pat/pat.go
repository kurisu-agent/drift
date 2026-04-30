// Package pat models GitHub PATs registered with lakitu and parses
// the human-readable settings page paste body that drives registration.
//
// The literal token never lives in a Pat — it lands in the chest under
// ChestRef, while the metadata captured here (owner, scopes, expiry)
// drives auto-resolution at `kart new` time and surfaces rotation
// reminders.
package pat

// FineGrainedPrefix is the literal prefix every fine-grained PAT carries.
// v1 only accepts fine-grained tokens; classic PATs (`ghp_*`) are
// rejected at registration time. This narrows the registry to the case
// that actually benefits from paste-driven metadata (classic PATs expose
// scope via `X-OAuth-Scopes` already).
const FineGrainedPrefix = "github_pat_"

// Pat is the on-disk shape for garage/pats/<slug>.yaml.
type Pat struct {
	Slug        string `yaml:"slug" json:"slug"`
	ChestRef    string `yaml:"chest_ref" json:"chest_ref"`
	Name        string `yaml:"name,omitempty" json:"name,omitempty"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	Owner       string `yaml:"owner,omitempty" json:"owner,omitempty"`
	ExpiresAt   string `yaml:"expires_at,omitempty" json:"expires_at,omitempty"`
	CreatedAt   string `yaml:"created_at,omitempty" json:"created_at,omitempty"`
	Scopes      Scopes `yaml:"scopes" json:"scopes"`
}

// Scopes captures user-asserted reach. The registry trusts these
// labels for matching; GitHub stays the source of truth for actual
// grants — a wrong record means a clone 401s, not a security hole.
//
// Repos vs ReposAll: a fine-grained PAT can either enumerate specific
// repositories or grant "all repositories owned by you." The settings
// page renders the latter as a sentinel paragraph rather than a list,
// so we capture it as a flag the resolver can match against any repo
// owned by Owner.
//
// Perms vs UserPerms: GitHub splits permissions into repository-scoped
// (which the kart.new resolver actually cares about — they decide
// whether a token can clone a given repo) and account-scoped (display
// only for v1). Keeping them in separate fields lets the resolver
// ignore user perms without filtering.
type Scopes struct {
	Repos     []string `yaml:"repos,omitempty" json:"repos,omitempty"`
	ReposAll  bool     `yaml:"repos_all,omitempty" json:"repos_all,omitempty"`
	Perms     []string `yaml:"perms,omitempty" json:"perms,omitempty"`
	UserPerms []string `yaml:"user_perms,omitempty" json:"user_perms,omitempty"`
}
