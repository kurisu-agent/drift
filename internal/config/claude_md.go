package config

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed CLAUDE.md
var embeddedClaudeMD []byte

// managedMarkerPrefix delimits the drift-managed header from user-owned
// content in files like CLAUDE.md. Everything up to and including the
// marker line is refreshed on every `lakitu init`; anything after it is
// preserved verbatim so operators can pin their own notes onto the file.
const managedMarkerPrefix = "<!-- drift:user"

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

// EnsureClaudeMD refreshes the managed header of $DRIFT_HOME/CLAUDE.md
// while preserving anything the operator added below the drift:user
// marker. Legacy files (pre-marker) stay untouched so pre-existing edits
// aren't clobbered — delete the file to opt into the new layout.
// Returns true iff the file was created or its managed header rewritten.
func EnsureClaudeMD(driftHome string) (bool, error) {
	path := filepath.Join(driftHome, "CLAUDE.md")
	return ensureManaged(path, driftHome, embeddedClaudeMD)
}

// ensureManaged is the shared write layer for drift-managed files that
// carry a `<!-- drift:user … -->` split marker. Kept here (rather than
// in a util package) because right now only CLAUDE.md uses it; promote
// later if a second caller appears.
func ensureManaged(path, parentDir string, embedded []byte) (bool, error) {
	existing, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return false, fmt.Errorf("config: stat %s: %w", path, err)
		}
		if mkErr := os.MkdirAll(parentDir, 0o750); mkErr != nil {
			return false, fmt.Errorf("config: create %s: %w", parentDir, mkErr)
		}
		if wErr := os.WriteFile(path, embedded, 0o600); wErr != nil {
			return false, fmt.Errorf("config: write %s: %w", path, wErr)
		}
		return true, nil
	}

	// File exists. If it lacks the marker, it's either a pre-marker
	// install or a file the user has hand-edited without keeping the
	// marker — either way, leave it alone.
	_, userTail, ok := splitOnManagedMarker(existing)
	if !ok {
		return false, nil
	}
	embeddedHeader, _, hasMarker := splitOnManagedMarker(embedded)
	if !hasMarker {
		// The embedded template is the source of truth for the marker's
		// placement; if a build of drift ever ships without it, every
		// subsequent init would silently lose operator content. Fail loud.
		return false, fmt.Errorf("config: embedded template for %s is missing the %q marker", path, managedMarkerPrefix)
	}

	merged := make([]byte, 0, len(embeddedHeader)+len(userTail))
	merged = append(merged, embeddedHeader...)
	merged = append(merged, userTail...)
	if bytes.Equal(merged, existing) {
		return false, nil
	}
	if err := os.WriteFile(path, merged, 0o600); err != nil {
		return false, fmt.Errorf("config: write %s: %w", path, err)
	}
	return true, nil
}

// splitOnManagedMarker returns (header, tail, true) when a line starting
// with managedMarkerPrefix is present: header contains everything up to
// and including the marker line's trailing newline, tail holds every
// byte after. Marker missing → (nil, nil, false). Match is prefix-based
// so the human-readable suffix of the marker line can evolve across
// releases without breaking detection on older on-disk copies.
func splitOnManagedMarker(content []byte) ([]byte, []byte, bool) {
	marker := []byte(managedMarkerPrefix)
	pos := 0
	for pos < len(content) {
		lineEnd := len(content)
		if nl := bytes.IndexByte(content[pos:], '\n'); nl >= 0 {
			lineEnd = pos + nl
		}
		if bytes.HasPrefix(content[pos:lineEnd], marker) {
			splitAt := lineEnd
			if splitAt < len(content) && content[splitAt] == '\n' {
				splitAt++
			}
			return content[:splitAt], content[splitAt:], true
		}
		if lineEnd == len(content) {
			break
		}
		pos = lineEnd + 1
	}
	return nil, nil, false
}
