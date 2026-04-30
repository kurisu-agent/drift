package drift

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/kurisu-agent/drift/internal/config"
)

// clientState is the client's local "what have I learned from the outside
// world recently?" file — $XDG_CONFIG_HOME/drift/state.json, alongside
// config.yaml. Written atomically so a crashed process can never leave a
// half-written state.json behind; read defensively so a corrupt file
// degrades to "treat as if nothing was ever checked" instead of aborting
// the command.
type clientState struct {
	LastUpdateCheck time.Time `json:"last_update_check,omitempty"`
	LatestVersion   string    `json:"latest_version,omitempty"`
	// LastBannerShown / BannerVersion gate how often the "update available"
	// banner prints. Storing the version we showed for lets a *new* release
	// land within the snooze window without being silenced — we only stay
	// quiet for a given (version, day) pair.
	LastBannerShown time.Time `json:"last_banner_shown,omitempty"`
	BannerVersion   string    `json:"banner_version,omitempty"`
}

func clientStatePath() (string, error) {
	dir, err := config.ClientConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "state.json"), nil
}

// loadClientState collapses every failure mode (missing file, bad JSON,
// permission error) into a zero-value result. Pre-dispatch hooks treat
// state.json as advisory: missing state simply means "check again".
func loadClientState() clientState {
	p, err := clientStatePath()
	if err != nil {
		return clientState{}
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) || err != nil {
		return clientState{}
	}
	var st clientState
	if json.Unmarshal(data, &st) != nil {
		return clientState{}
	}
	return st
}

func saveClientState(st clientState) error {
	p, err := clientStatePath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	// WriteFileAtomic creates parent dirs at 0o700, matching the
	// convention used for other client-side config under $XDG_CONFIG_HOME.
	return config.WriteFileAtomic(p, data, 0o600)
}
