package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// ClientConfigDir honors XDG_CONFIG_HOME, falling back to $HOME/.config.
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

func ClientConfigPath() (string, error) {
	dir, err := ClientConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.yaml"), nil
}

// GarageDir uses os.UserHomeDir so t.Setenv("HOME", ...) works in tests.
func GarageDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: resolve home directory: %w", err)
	}
	return filepath.Join(home, ".drift", "garage"), nil
}

func ServerConfigPath() (string, error) {
	dir, err := GarageDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.yaml"), nil
}
