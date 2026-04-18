package config

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed CLAUDE.md
var embeddedClaudeMD []byte

// ClaudeMDPath returns the path to the drift CLAUDE.md — the agent context
// file read by `claude --dangerously-skip-permissions` when launched via
// `drift ai`. It lives at $HOME/.drift/CLAUDE.md, alongside the garage.
func ClaudeMDPath() (string, error) {
	home, err := DriftHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "CLAUDE.md"), nil
}

// DriftHomeDir is $HOME/.drift — the parent of the garage. The garage is a
// subdirectory (so lakitu can manage its internals independently), but
// CLAUDE.md sits one level up because that's the cwd `drift ai` drops into.
func DriftHomeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: resolve home directory: %w", err)
	}
	return filepath.Join(home, ".drift"), nil
}

// EnsureClaudeMD writes the embedded CLAUDE.md to driftHome/CLAUDE.md if the
// file doesn't already exist. Pre-existing files are preserved so a user who
// has edited their CLAUDE.md never has their changes clobbered by a re-init.
// Returns true iff this call created the file.
func EnsureClaudeMD(driftHome string) (bool, error) {
	path := filepath.Join(driftHome, "CLAUDE.md")
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("config: stat %s: %w", path, err)
	}
	if err := os.MkdirAll(driftHome, 0o750); err != nil {
		return false, fmt.Errorf("config: create %s: %w", driftHome, err)
	}
	if err := os.WriteFile(path, embeddedClaudeMD, 0o600); err != nil {
		return false, fmt.Errorf("config: write %s: %w", path, err)
	}
	return true, nil
}
