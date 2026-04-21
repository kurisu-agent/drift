package devpod

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
)

// AgentWorkspaceEntry pairs a parsed workspace record with the devpod
// context directory it was found in. drift migrate uses the context name
// to disambiguate same-named workspaces across contexts and to call the
// right `devpod --context` when the user opts to delete the old one.
type AgentWorkspaceEntry struct {
	Context   string
	Workspace Workspace
}

// ParseAgentWorkspaceJSON reads one `workspace.json` from devpod's agent
// contexts tree. Agent-side records are wrapped:
//
//	{"workspaceOrigin": "...", "workspace": { ...actual fields... }}
//
// but some tooling emits the inner object directly (the same shape devpod
// writes on the client side). We accept both so tests and future devpod
// refactors don't break migrate.
//
// Only the fields Workspace declares are read; everything else in the
// file is silently ignored so migrate rides through additive devpod
// format changes.
func ParseAgentWorkspaceJSON(r io.Reader) (Workspace, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return Workspace{}, err
	}
	var wrap struct {
		Workspace *Workspace `json:"workspace"`
	}
	if err := json.Unmarshal(raw, &wrap); err == nil && wrap.Workspace != nil && wrap.Workspace.ID != "" {
		return *wrap.Workspace, nil
	}
	var ws Workspace
	if err := json.Unmarshal(raw, &ws); err != nil {
		return Workspace{}, err
	}
	return ws, nil
}

// AgentContextsRoot returns the directory devpod writes agent-side
// workspace records under. Honors $DEVPOD_HOME for parity with devpod's
// own resolution; falls back to $HOME/.devpod. Returns "" on a hostile
// environment with no home — callers treat that as "nothing to list".
func AgentContextsRoot() string {
	if h := os.Getenv("DEVPOD_HOME"); h != "" {
		return filepath.Join(h, "agent", "contexts")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".devpod", "agent", "contexts")
}

// ListAgentWorkspaces walks every context under root and returns one
// entry per parseable workspace.json. A nonexistent root returns an
// empty slice, not an error — drift migrate on a server that has never
// run devpod should quietly report "nothing to migrate" rather than
// surfacing an opaque stat failure.
//
// Unparseable files (corrupt JSON, unexpected shape) are skipped
// silently; a single stale record should not break discovery of the
// other candidates.
func ListAgentWorkspaces(root string) ([]AgentWorkspaceEntry, error) {
	if root == "" {
		return nil, nil
	}
	contexts, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []AgentWorkspaceEntry
	for _, c := range contexts {
		if !c.IsDir() {
			continue
		}
		wsDir := filepath.Join(root, c.Name(), "workspaces")
		entries, err := os.ReadDir(wsDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			path := filepath.Join(wsDir, e.Name(), "workspace.json")
			f, err := os.Open(path)
			if err != nil {
				continue
			}
			ws, perr := ParseAgentWorkspaceJSON(f)
			_ = f.Close()
			if perr != nil || ws.ID == "" {
				continue
			}
			out = append(out, AgentWorkspaceEntry{Context: c.Name(), Workspace: ws})
		}
	}
	return out, nil
}
