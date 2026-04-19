package config

import "fmt"

// Server holds the fields settable via `lakitu config set` / config.set RPC.
type Server struct {
	DefaultTune      string      `yaml:"default_tune"`
	DefaultCharacter string      `yaml:"default_character"`
	NixCacheURL      string      `yaml:"nix_cache_url"`
	Chest            ChestConfig `yaml:"chest"`
}

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
