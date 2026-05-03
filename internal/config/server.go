package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/kurisu-agent/drift/internal/icons"
	"github.com/kurisu-agent/drift/internal/name"
)

// chestRefPrefix mirrors internal/chest.RefPrefix. Duplicated here as
// a literal to avoid an import cycle (chest depends on config). Kept in
// sync by review; unit tests in both packages would catch a divergence.
const chestRefPrefix = "chest:"

// Server holds the fields settable via `lakitu config set` / config.set RPC.
type Server struct {
	// Name identifies this circuit to clients. Empty on disk means "derive
	// from hostname at load time"; operators can override with `drift
	// circuit set name <name>` or by editing the config.
	Name string `yaml:"name,omitempty"`
	// Icon is either a nerd-font glyph name ("dev-go") or a single emoji
	// ("🚀"). Empty means no icon. Resolved client-side via internal/icons
	// — the config stores the source-of-truth string, not the rendered
	// glyph, so font swaps and emoji renderers don't break the config file.
	Icon             string      `yaml:"icon,omitempty"`
	DefaultTune      string      `yaml:"default_tune"`
	DefaultCharacter string      `yaml:"default_character"`
	NixCacheURL      string      `yaml:"nix_cache_url"`
	Chest            ChestConfig `yaml:"chest"`
	// DenyLiterals, when set, MUST be a `chest:<name>` reference. The
	// referenced chest entry holds a newline-separated deny-list (one
	// fixed-string pattern per line, `#` for comments) that lakitu
	// drops into every kart at `~/.claude/deny-literals.txt` via the
	// `claudeCode` seed. Paired with the always-
	// installed PreToolUse hook, this gives every Claude Code session
	// inside a kart the same forbidden-literal guardrail the workstation
	// has. Empty = no list dropped, hook silently no-ops. See
	// plans/20-kart-deny-literals.md.
	DenyLiterals string `yaml:"deny_literals,omitempty"`
	// LakituGitHubAPIPAT, when set, MUST be a `chest:<name>` reference.
	// The referenced chest entry holds a GitHub PAT that lakitu sends as
	// `Authorization: Bearer …` on outbound HTTPS calls to api.github.com,
	// github.com, and *.githubusercontent.com — currently the devpod and
	// filebrowser binary bootstraps and the drift release-asset fetch.
	// Authenticating these calls lifts GitHub's anonymous 60-req/hr
	// per-IP rate limit to 5000-req/hr per token, which matters on busy
	// circuits or when many karts boot at once. Empty = unauthenticated
	// (works fine until the IP gets rate-limited). The PAT only needs
	// public-repo read scope; nothing here writes to GitHub.
	LakituGitHubAPIPAT string `yaml:"lakitu_github_api_pat,omitempty"`
}

// CircuitNameRE is the shared slug shape for circuit names, mirrored on
// the client side for local validation of `circuit add`/`circuit set`.
// Kept as a compiled alias so external callers that match against the
// raw regexp continue to work; new callers should prefer
// name.Validate("circuit", s), which also rejects the reserved slugs.
var CircuitNameRE = regexp.MustCompile(name.Pattern)

type ChestConfig struct {
	Backend string `yaml:"backend"`
}

const ChestBackendYAMLFile = "yamlfile"

// Reject unknown backends so typos surface at `lakitu init` rather than
// the first `chest.set`.
var validChestBackends = map[string]struct{}{
	ChestBackendYAMLFile: {},
}

// DefaultServer is what `lakitu init` writes. default_tune points at a
// tune name; the resolver looks up tunes/<name>.yaml and falls through
// to "no tune" if the file doesn't exist (so a bare init isn't broken
// before the operator creates any tunes).
func DefaultServer() *Server {
	return &Server{
		DefaultTune: "nixenv",
		Chest: ChestConfig{
			Backend: ChestBackendYAMLFile,
		},
	}
}

func (s *Server) Validate() error {
	if s.Name != "" {
		if err := name.Validate("circuit", s.Name); err != nil {
			return fmt.Errorf("config: %w", err)
		}
	}
	if err := icons.Validate(s.Icon); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if s.DefaultTune == "" {
		return fmt.Errorf("config: default_tune is required")
	}
	if s.Chest.Backend == "" {
		return fmt.Errorf("config: chest.backend is required")
	}
	if _, ok := validChestBackends[s.Chest.Backend]; !ok {
		return fmt.Errorf("config: chest.backend %q is not a supported backend", s.Chest.Backend)
	}
	if s.DenyLiterals != "" {
		if !strings.HasPrefix(s.DenyLiterals, chestRefPrefix) || len(s.DenyLiterals) == len(chestRefPrefix) {
			return fmt.Errorf("config: deny_literals must be a chest reference of the form %q<name>; literal lists are not accepted", chestRefPrefix)
		}
	}
	if s.LakituGitHubAPIPAT != "" {
		if !strings.HasPrefix(s.LakituGitHubAPIPAT, chestRefPrefix) || len(s.LakituGitHubAPIPAT) == len(chestRefPrefix) {
			return fmt.Errorf("config: lakitu_github_api_pat must be a chest reference of the form %q<name>; raw tokens are not accepted to keep them out of the on-disk config", chestRefPrefix)
		}
	}
	return nil
}

// ResolveName returns the configured Name, or — when empty — the short
// form of the system hostname lowercased. SSH alias `drift.<name>` can't
// contain dots, so we keep only the first DNS label (FQDNs become their
// leading label). If the resulting value doesn't fit the circuit name
// shape, fall back to the literal `circuit` and rely on the operator to
// override via `drift circuit set name`.
func (s *Server) ResolveName() string {
	if s.Name != "" {
		return s.Name
	}
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "circuit"
	}
	if i := strings.IndexByte(h, '.'); i >= 0 {
		h = h[:i]
	}
	h = strings.ToLower(h)
	if err := name.Validate("circuit", h); err != nil {
		return "circuit"
	}
	return h
}

// LoadServer: unlike the client config, a missing server config is an
// error — `lakitu init` is responsible for creating it.
func LoadServer(path string) (*Server, error) {
	var s Server
	found, err := loadYAMLStrict(path, &s)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("config: %s does not exist (run `lakitu init`)", path)
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

// SaveServer writes 0644 — no secrets, only pointers to them.
func SaveServer(path string, s *Server) error {
	if err := s.Validate(); err != nil {
		return err
	}
	return marshalAndWrite(path, s, 0o644)
}
