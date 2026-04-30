package kart

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tailscale/hujson"

	"github.com/kurisu-agent/drift/internal/devpod"
)

// ProbeForwardPorts reads the kart's devcontainer.json (in the cloned
// workspace, the two standard candidate paths) and extracts the
// forwardPorts entries as TCP port numbers. The devcontainer spec allows
// forwardPorts to be a list of either numbers (`3000`) or strings —
// either bare (`"3000"`), with a label suffix (`"3000:web"`), or with a
// host (`"localhost:3000"`); all of those collapse to the same TCP port
// from drift's perspective since we always forward `localhost:<n>` to
// `localhost:<n>` inside the kart.
//
// Best-effort: any missing file, parse failure, or unrecognized entry
// returns the empty slice. Drift never *fails* a connect because the
// devcontainer can't be parsed; the user's forwardPorts feature degrades
// to "no automatic forwards" rather than blocking the shell launch.
func ProbeForwardPorts(kart string) []int {
	dir := filepath.Join(devpod.AgentContextsRoot(), "default", "workspaces", kart, "content")
	for _, candidate := range []string{".devcontainer/devcontainer.json", ".devcontainer.json"} {
		raw, err := os.ReadFile(filepath.Join(dir, candidate))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil
		}
		standard, err := hujson.Standardize(raw)
		if err != nil {
			return nil
		}
		var dc struct {
			ForwardPorts []json.RawMessage `json:"forwardPorts"`
		}
		if err := json.Unmarshal(standard, &dc); err != nil {
			return nil
		}
		return parseForwardPortsList(dc.ForwardPorts)
	}
	return nil
}

func parseForwardPortsList(raws []json.RawMessage) []int {
	if len(raws) == 0 {
		return nil
	}
	out := make([]int, 0, len(raws))
	seen := make(map[int]bool, len(raws))
	for _, raw := range raws {
		port, ok := parseForwardPortEntry(raw)
		if !ok {
			continue
		}
		if seen[port] {
			continue
		}
		seen[port] = true
		out = append(out, port)
	}
	return out
}

func parseForwardPortEntry(raw json.RawMessage) (int, bool) {
	// Numeric: `3000`.
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		if n >= 1 && n <= 65535 {
			return n, true
		}
		return 0, false
	}
	// String: `"3000"`, `"3000:web"`, or `"host:3000"`.
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return 0, false
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	// "host:port[:label]" — the port is the last all-digit segment.
	for _, segment := range splitColon(s) {
		if v, err := strconv.Atoi(segment); err == nil {
			if v >= 1 && v <= 65535 {
				return v, true
			}
		}
	}
	return 0, false
}

func splitColon(s string) []string {
	parts := strings.Split(s, ":")
	// Walk from the right so a leading host segment doesn't shadow the
	// port number (e.g. "localhost:3000" → return "3000" first).
	out := make([]string, 0, len(parts))
	for i := len(parts) - 1; i >= 0; i-- {
		out = append(out, parts[i])
	}
	return out
}
