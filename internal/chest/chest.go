// Package chest defines the pluggable secret store used by lakitu to resolve
// `chest:<name>` references attached to characters and by the `chest.*` RPC
// methods. MVP ships a single backend, [YAMLFileBackend], backed by a mode
// 0600 YAML file under the garage.
//
// Backends are selected from the server config's `chest.backend` field.
package chest

import (
	"fmt"

	"github.com/kurisu-agent/drift/internal/config"
)

// Backend is the minimal interface every chest backend implements. The API
// is sync and value-oriented because the yamlfile backend is the only MVP
// implementation and it touches a single local file.
//
// Names must satisfy the drift identifier regex (validated by callers); the
// backend itself is not responsible for name validation beyond reporting a
// not-found when Get/Remove hit a key that isn't stored.
type Backend interface {
	// Set stores value under name, overwriting any existing entry.
	Set(name string, value []byte) error
	// Get returns the value stored under name. Returns a [*rpcerr.Error]
	// with type "chest_entry_not_found" when the key is missing.
	Get(name string) ([]byte, error)
	// List returns every stored key in lexicographic order. Values are
	// never returned.
	List() ([]string, error)
	// Remove deletes the entry stored under name. Missing keys return a
	// not-found rpcerr.
	Remove(name string) error
}

// Open constructs the Backend described by cfg. Unknown backend values are
// rejected up front so downstream code can rely on the returned Backend
// being usable.
func Open(garageDir string, cfg config.ChestConfig) (Backend, error) {
	switch cfg.Backend {
	case config.ChestBackendYAMLFile, "":
		return NewYAMLFile(garageDir), nil
	default:
		return nil, fmt.Errorf("chest: unsupported backend %q", cfg.Backend)
	}
}
