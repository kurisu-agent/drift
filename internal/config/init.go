package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// GarageSubdirs is the canonical set of subdirectories created under
// ~/.drift/garage by `lakitu init`. Order is not significant — MkdirAll is
// idempotent — but the list is declared once so the testscripts and the
// init handler agree on what a freshly bootstrapped garage looks like.
var GarageSubdirs = []string{
	"tunes",
	"characters",
	"chest",
	"karts",
}

// InitResult describes what InitGarage did. It is returned by both the
// named `lakitu init` subcommand and the `server.init` RPC handler so
// callers can report created / preserved paths to the user.
type InitResult struct {
	GarageDir string `json:"garage_dir"`
	// Created lists garage-relative paths that this call brought into
	// existence (directories and/or the config file). Empty on a re-run
	// of an already-initialized garage.
	Created []string `json:"created,omitempty"`
}

// InitGarage bootstraps ~/.drift/garage at root so that the server-side
// file layout from plans/PLAN.md § Server state layout exists. It is
// idempotent: re-running reports no new Created entries.
//
// The chest directory is created with mode 0700 because the MVP envfile
// backend keeps plain-text secrets under it. Other directories use 0755.
func InitGarage(root string) (*InitResult, error) {
	res := &InitResult{GarageDir: root}

	if err := ensureDir(root, 0o755, &res.Created, ""); err != nil {
		return nil, err
	}
	for _, sub := range GarageSubdirs {
		mode := os.FileMode(0o755)
		if sub == "chest" {
			mode = 0o700
		}
		if err := ensureDir(filepath.Join(root, sub), mode, &res.Created, sub); err != nil {
			return nil, err
		}
	}

	cfgPath := filepath.Join(root, "config.yaml")
	created, err := ensureDefaultServerConfig(cfgPath)
	if err != nil {
		return nil, err
	}
	if created {
		res.Created = append(res.Created, "config.yaml")
	}
	return res, nil
}

// ensureDir creates dir with mode if missing, tracking garage-relative
// rel-paths in the InitResult. Existing directories are left untouched,
// even if their permissions drift from mode — this keeps the init safe to
// re-run on a garage the user has tightened manually.
func ensureDir(dir string, mode os.FileMode, created *[]string, rel string) error {
	info, err := os.Stat(dir)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("config: %s exists but is not a directory", dir)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("config: stat %s: %w", dir, err)
	}
	if err := os.MkdirAll(dir, mode); err != nil {
		return fmt.Errorf("config: create %s: %w", dir, err)
	}
	// MkdirAll obeys umask; force the requested mode explicitly so the
	// chest directory always ends up 0700 regardless of the user's umask.
	if err := os.Chmod(dir, mode); err != nil {
		return fmt.Errorf("config: chmod %s: %w", dir, err)
	}
	if rel != "" {
		*created = append(*created, rel)
	}
	return nil
}

// ensureDefaultServerConfig writes DefaultServer() to path when path is
// absent. A pre-existing file is left untouched so a user who has edited
// their config never has their changes clobbered by a `lakitu init` rerun.
func ensureDefaultServerConfig(path string) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("config: stat %s: %w", path, err)
	}
	if err := SaveServer(path, DefaultServer()); err != nil {
		return false, err
	}
	return true, nil
}
