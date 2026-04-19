package config

import (
	"fmt"
	"os"
	"regexp"
)

// Server holds the fields settable via `lakitu config set` / config.set RPC.
type Server struct {
	// Name identifies this circuit to clients. Empty on disk means "derive
	// from hostname at load time"; operators can override with `drift
	// circuit set name <name>` or by editing the config.
	Name             string      `yaml:"name,omitempty"`
	DefaultTune      string      `yaml:"default_tune"`
	DefaultCharacter string      `yaml:"default_character"`
	NixCacheURL      string      `yaml:"nix_cache_url"`
	Chest            ChestConfig `yaml:"chest"`
}

// CircuitNameRE is the shared slug shape for circuit names, mirrored on
// the client side for local validation of `circuit add`/`circuit set`.
var CircuitNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

type ChestConfig struct {
	Backend string `yaml:"backend"`
}

const ChestBackendYAMLFile = "yamlfile"

// Reject unknown backends so typos surface at `lakitu init` rather than
// the first `chest.set`.
var validChestBackends = map[string]struct{}{
	ChestBackendYAMLFile: {},
}

// DefaultServer is what `lakitu init` writes. "default" as default_tune
// means a user-created tune literally named "default" becomes implicit
// when --tune is omitted.
func DefaultServer() *Server {
	return &Server{
		DefaultTune: "default",
		Chest: ChestConfig{
			Backend: ChestBackendYAMLFile,
		},
	}
}

func (s *Server) Validate() error {
	if s.Name != "" && !CircuitNameRE.MatchString(s.Name) {
		return fmt.Errorf("config: name %q invalid (must match %s)", s.Name, CircuitNameRE.String())
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
	if i := indexByte(h, '.'); i >= 0 {
		h = h[:i]
	}
	h = toLowerASCII(h)
	if !CircuitNameRE.MatchString(h) {
		return "circuit"
	}
	return h
}

// indexByte / toLowerASCII keep this file std-lib-only; the strings
// package would be fine but these are ~3 lines each and mirror the tiny
// helper style elsewhere in config/.
func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func toLowerASCII(s string) string {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return string(out)
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
