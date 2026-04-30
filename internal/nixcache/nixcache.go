// Package nixcache exposes the on-disk marker file written by the lakitu
// NixOS module's `services.lakitu.nixCache` activation script (plan 17).
// Both the lakitu CLI's `nix-cache info` subcommand and the server's
// kart.new handler read this file — the former to print a paste-ready
// snippet, the latter to auto-inject substituters into per-kart Nix
// feature configs.
package nixcache

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// DefaultMarkerPath matches the path the lakitu NixOS module writes to.
// The file is on tmpfs (/run) so it's regenerated on every activation,
// reflecting the current `services.lakitu.nixCache.*` options without a
// lakitu restart.
const DefaultMarkerPath = "/run/lakitu/nix-cache.json"

// Marker is the on-disk shape of the nix-cache marker file. URL is the
// kart-reachable substituter URL (defaults to http://172.17.0.1:5000 on
// single-host docker circuits), Pubkey is the Nix signing public key,
// and Upstream is the list of upstream substituters this cache forwards
// to (defaults to [https://cache.nixos.org]).
type Marker struct {
	URL      string   `json:"url"`
	Pubkey   string   `json:"pubkey"`
	Upstream []string `json:"upstream"`
}

// Read parses the marker file at path. A missing file is reported as
// ErrNotConfigured so callers can distinguish "cache not enabled" (a
// soft state, expected on circuits without the option set) from real
// I/O errors.
func Read(path string) (Marker, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Marker{}, ErrNotConfigured
		}
		return Marker{}, fmt.Errorf("read %s: %w", path, err)
	}
	var m Marker
	if err := json.Unmarshal(data, &m); err != nil {
		return Marker{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if m.URL == "" || m.Pubkey == "" {
		return Marker{}, fmt.Errorf("nix-cache marker at %s is incomplete (url/pubkey empty); rebuild may be in progress", path)
	}
	return m, nil
}

// ErrNotConfigured is returned by Read when the marker file does not
// exist — i.e. `services.lakitu.nixCache.enable` is not set on this
// circuit. Callers should treat this as a no-op rather than a failure.
var ErrNotConfigured = errors.New("nixcache: marker file not present (services.lakitu.nixCache.enable not set)")
