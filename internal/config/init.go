package config

import (
	"fmt"
	"os"
	"path/filepath"
)

var GarageSubdirs = []string{
	"tunes",
	"characters",
	"chest",
	"karts",
}

type InitResult struct {
	GarageDir string `json:"garage_dir"`
	// Created lists garage-relative paths brought into existence by this
	// call. Empty on a re-run of an initialized garage.
	Created []string `json:"created,omitempty"`
}

// InitGarage is idempotent. The chest directory is 0700 because the MVP
// yamlfile backend keeps plain-text secrets under it.
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

// InitGarageFull runs the full `lakitu init` filesystem sequence: the
// garage tree plus the $DRIFT_HOME sibling CLAUDE.md. The returned result
// reports every path touched — garage-relative entries from InitGarage
// plus "../CLAUDE.md" when that file was created.
//
// Separated from InitGarage so the server.init RPC and `lakitu init` CLI
// share one code path; the CLI prints from Created, the RPC hands the
// result back to the drift client which renders the same list.
func InitGarageFull(root, driftHome string) (*InitResult, error) {
	res, err := InitGarage(root)
	if err != nil {
		return nil, err
	}
	if created, cerr := EnsureClaudeMD(driftHome); cerr != nil {
		return nil, cerr
	} else if created {
		res.Created = append(res.Created, "../CLAUDE.md")
	}
	if created, rerr := EnsureRunsYAML(driftHome); rerr != nil {
		return nil, rerr
	} else if created {
		res.Created = append(res.Created, "../runs.yaml")
	}
	return res, nil
}

// ensureDir leaves existing directories untouched — even if their
// permissions drift from mode — so init stays safe to re-run on a garage
// the user tightened manually.
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
	// MkdirAll obeys umask; force the requested mode so the chest dir
	// ends up 0700 regardless of the user's umask.
	if err := os.Chmod(dir, mode); err != nil {
		return fmt.Errorf("config: chmod %s: %w", dir, err)
	}
	if rel != "" {
		*created = append(*created, rel)
	}
	return nil
}

// ensureDefaultServerConfig leaves a pre-existing file untouched so a
// user's edits never get clobbered by a `lakitu init` rerun.
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
