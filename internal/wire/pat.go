package wire

import "github.com/kurisu-agent/drift/internal/pat"

// PatPutParams is shared by pat.new and pat.update. Token rides in the
// JSON-RPC body so the secret never lands on argv. On update, an empty
// Token means "leave the chest entry untouched, only refresh metadata."
type PatPutParams struct {
	Slug        string   `json:"slug"`
	Token       string   `json:"token,omitempty"`
	Name        string   `json:"name,omitempty"`
	Description string   `json:"description,omitempty"`
	Owner       string   `json:"owner,omitempty"`
	ExpiresAt   string   `json:"expires_at,omitempty"`
	CreatedAt   string   `json:"created_at,omitempty"`
	Repos       []string `json:"repos,omitempty"`
	ReposAll    bool     `json:"repos_all,omitempty"`
	Perms       []string `json:"perms,omitempty"`
	UserPerms   []string `json:"user_perms,omitempty"`
}

// PatResult bundles the slug with the persisted yaml. Mirrors the shape
// of CharacterResult / TuneResult so table renderers can stay generic.
type PatResult struct {
	Slug string  `json:"slug"`
	Pat  pat.Pat `json:"pat"`
}

// PatSlugOnly is the params shape for pat.remove and any future
// per-slug RPC that doesn't carry metadata.
type PatSlugOnly struct {
	Slug string `json:"slug"`
}

// PatFindForCloneParams asks the registry which registered PATs cover a
// given github clone target. Owner and Repo are the bare segments
// (`<owner>/<repo>`); the caller has already parsed the clone URL.
type PatFindForCloneParams struct {
	Owner string `json:"owner"`
	Repo  string `json:"repo"`
}
