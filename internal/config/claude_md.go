package config

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed CLAUDE.md
var embeddedClaudeMD []byte

// ClaudeMDPath: the agent context file `drift run ai` drops into. Lives at
// $HOME/.drift/CLAUDE.md alongside the garage.
func ClaudeMDPath() (string, error) {
	home, err := DriftHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "CLAUDE.md"), nil
}

// DriftHomeDir = $HOME/.drift. CLAUDE.md sits here (one level up from
// the garage) because that's the cwd `drift run ai` drops into.
func DriftHomeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: resolve home directory: %w", err)
	}
	return filepath.Join(home, ".drift"), nil
}

// EnsureClaudeMD preserves pre-existing files so user edits aren't
// clobbered by a re-init. Returns true iff this call created the file.
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
