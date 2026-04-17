package config

import "fmt"

// Server is the circuit-side config, mirroring plans/PLAN.md § Server state
// layout — the fields settable via `lakitu config set` / the `config.set`
// RPC method.
type Server struct {
	DefaultTune      string      `yaml:"default_tune"`
	DefaultCharacter string      `yaml:"default_character"`
	NixCacheURL      string      `yaml:"nix_cache_url"`
	Chest            ChestConfig `yaml:"chest"`
}

// ChestConfig selects the active chest backend and holds any backend-
// specific knobs. MVP ships only the yamlfile backend.
type ChestConfig struct {
	Backend string `yaml:"backend"`
}

// ChestBackendYAMLFile is the MVP backend name. The YAML format replaces
// the earlier shell-quoted envfile so multi-line secrets (SSH keys,
// PEM-encoded PATs) round-trip via YAML's block scalars without custom
// escaping. Other backends (age, 1password, vault) are reserved for
// post-MVP work.
const ChestBackendYAMLFile = "yamlfile"

// validChestBackends is the exhaustive set of acceptable backend values.
// Validation rejects anything outside this list so typos surface during
// `lakitu init` instead of at `chest.set` time.
var validChestBackends = map[string]struct{}{
	ChestBackendYAMLFile: {},
}

// DefaultServer is the config that `lakitu init` writes on a fresh garage.
// default_tune is "default" to match plans/PLAN.md § Flag composition — a
// tune literally named "default" becomes the implicit preset when
// --tune is omitted.
func DefaultServer() *Server {
	return &Server{
		DefaultTune: "default",
		Chest: ChestConfig{
			Backend: ChestBackendYAMLFile,
		},
	}
}

// Validate checks field-level invariants. Callers are expected to run this
// after loading and before writing.
func (s *Server) Validate() error {
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

// LoadServer decodes a server config from path. Unlike the client config, a
// missing server config is an error — `lakitu init` is responsible for
// creating it.
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

// SaveServer atomically writes s to path after validating it. The file is
// 0644 — it contains no secrets, only pointers to them.
func SaveServer(path string, s *Server) error {
	if err := s.Validate(); err != nil {
		return err
	}
	return marshalAndWrite(path, s, 0o644)
}
