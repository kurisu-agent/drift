package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// ClientConfigDir is the drift client's config directory. It honors
// XDG_CONFIG_HOME when set, falling back to $HOME/.config per the XDG Base
// Directory Specification.
func ClientConfigDir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "drift"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: resolve home directory: %w", err)
	}
	return filepath.Join(home, ".config", "drift"), nil
}

// ClientConfigPath is the full path to the client's config.yaml.
func ClientConfigPath() (string, error) {
	dir, err := ClientConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.yaml"), nil
}

// GarageDir is the lakitu garage root on a circuit — always $HOME/.drift/garage.
// $HOME is resolved by os.UserHomeDir so tests using t.Setenv("HOME", ...) work.
func GarageDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: resolve home directory: %w", err)
	}
	return filepath.Join(home, ".drift", "garage"), nil
}

// ServerConfigPath is the full path to the server's config.yaml inside the garage.
func ServerConfigPath() (string, error) {
	dir, err := GarageDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.yaml"), nil
}
