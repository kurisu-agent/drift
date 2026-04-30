// Package chest defines the pluggable secret store backing `chest:<name>`
// character references and the `chest.*` RPC methods. MVP ships a single
// backend, [YAMLFileBackend], selected via the server config's chest.backend.
package chest

import (
	"fmt"
	"strings"

	"github.com/kurisu-agent/drift/internal/config"
)

// RefPrefix is the scheme prefix on every chest reference (`chest:<name>`).
// Used by callers that persist or compare raw ref strings; prefer ParseRef
// over string-literal HasPrefix/TrimPrefix pairs at call sites.
const RefPrefix = "chest:"

// ParseRef returns (name, true) when s is a chest reference ("chest:foo" →
// "foo"). Returns ("", false) for any non-prefixed input — including the
// empty string — so callers can branch on ok without re-checking HasPrefix.
func ParseRef(s string) (name string, ok bool) {
	if !strings.HasPrefix(s, RefPrefix) {
		return "", false
	}
	return strings.TrimPrefix(s, RefPrefix), true
}

// Backend is the minimal interface. Names are validated by callers; the
// backend only reports not-found on missing keys in Get/Remove.
type Backend interface {
	Set(name string, value []byte) error
	// Get returns *rpcerr.Error with type "chest_entry_not_found" when missing.
	Get(name string) ([]byte, error)
	// List returns keys in lexicographic order. Never returns values.
	List() ([]string, error)
	Remove(name string) error
}

func Open(garageDir string, cfg config.ChestConfig) (Backend, error) {
	switch cfg.Backend {
	case config.ChestBackendYAMLFile, "":
		return NewYAMLFile(garageDir), nil
	default:
		return nil, fmt.Errorf("chest: unsupported backend %q", cfg.Backend)
	}
}
