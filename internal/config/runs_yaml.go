package config

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed runs.yaml
var embeddedRunsYAML []byte

// DefaultRunsYAML returns the embedded default registry bytes. The server
// parses these alongside the user's runs.yaml so schema additions (new
// arg prompts, etc.) can be back-filled onto built-in entries that were
// seeded before the feature shipped — see run.MergeBuiltinDefaults.
func DefaultRunsYAML() []byte {
	return embeddedRunsYAML
}

// RunsYAMLPath is the canonical server-side path for the run registry.
func RunsYAMLPath() (string, error) {
	home, err := DriftHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "runs.yaml"), nil
}

// EnsureRunsYAML writes the embedded default registry to
// $DRIFT_HOME/runs.yaml when the file does not exist. Pre-existing files
// are preserved — users edit this file and re-running `lakitu init`
// must not clobber their changes. Returns true iff the file was created.
func EnsureRunsYAML(driftHome string) (bool, error) {
	return ensureEmbedded(
		filepath.Join(driftHome, "runs.yaml"),
		driftHome,
		embeddedRunsYAML,
	)
}

func ensureEmbedded(path, parentDir string, content []byte) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("config: stat %s: %w", path, err)
	}
	if err := os.MkdirAll(parentDir, 0o750); err != nil {
		return false, fmt.Errorf("config: create %s: %w", parentDir, err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return false, fmt.Errorf("config: write %s: %w", path, err)
	}
	return true, nil
}
