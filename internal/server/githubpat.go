package server

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/kurisu-agent/drift/internal/chest"
	"github.com/kurisu-agent/drift/internal/config"
)

// ResolveLakituGitHubAPIPAT loads the lakitu server config at
// serverCfgPath and, when LakituGitHubAPIPAT is set, dechests it
// against the chest backend rooted at garageDir. Returns ("", nil)
// when the field is empty — that's the supported "unauthenticated
// GitHub API calls" mode and not an error. Errors are returned only
// when the config or chest say something is configured but resolving
// it fails (missing chest entry, unreadable backend); callers can
// surface those to the operator.
//
// openChest, when nil, defaults to [chest.Open]. Tests inject a
// fake to avoid touching the real garage.
func ResolveLakituGitHubAPIPAT(
	serverCfgPath, garageDir string,
	openChest func(string, config.ChestConfig) (chest.Backend, error),
) (string, error) {
	// Pre-init state — `lakitu init` hasn't run yet, no config file.
	// Treat as "no PAT configured" silently; this is the normal state
	// during early bootstrap (e.g. integration tests using the lakitu
	// binary against a scratch garage) and shouldn't print warnings.
	if _, err := os.Stat(serverCfgPath); errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	srv, err := config.LoadServer(serverCfgPath)
	if err != nil {
		// Config exists but is malformed or unreadable — surface to
		// the caller, which decides whether to log + degrade.
		return "", err
	}
	if srv.LakituGitHubAPIPAT == "" {
		return "", nil
	}
	name, ok := chest.ParseRef(srv.LakituGitHubAPIPAT)
	if !ok {
		// Validate() rejects this on load, so we should never reach
		// here in practice — but treat it defensively.
		return "", fmt.Errorf("config: lakitu_github_api_pat: not a chest reference: %q", srv.LakituGitHubAPIPAT)
	}
	if openChest == nil {
		openChest = chest.Open
	}
	backend, err := openChest(garageDir, srv.Chest)
	if err != nil {
		return "", fmt.Errorf("chest: open: %w", err)
	}
	val, err := backend.Get(name)
	if err != nil {
		return "", fmt.Errorf("chest: get %q: %w", name, err)
	}
	return string(val), nil
}
