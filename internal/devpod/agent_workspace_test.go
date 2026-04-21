package devpod_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kurisu-agent/drift/internal/devpod"
)

func TestParseAgentWorkspaceJSON_Wrapped(t *testing.T) {
	const body = `{
		"workspaceOrigin": "/home/alice/.devpod/contexts/default/workspaces/research/workspace.json",
		"workspace": {
			"id": "research",
			"uid": "default-re-a9a30",
			"source": {"gitRepository": "https://github.com/example-org/research"},
			"provider": {"name": "ssh"},
			"creationTimestamp": "2026-03-15T07:43:50Z"
		}
	}`
	ws, err := devpod.ParseAgentWorkspaceJSON(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ws.ID != "research" {
		t.Errorf("ID = %q, want research", ws.ID)
	}
	if ws.Source.GitRepository != "https://github.com/example-org/research" {
		t.Errorf("GitRepository = %q", ws.Source.GitRepository)
	}
	if ws.Provider.Name != "ssh" {
		t.Errorf("Provider.Name = %q, want ssh", ws.Provider.Name)
	}
}

func TestParseAgentWorkspaceJSON_Bare(t *testing.T) {
	const body = `{
		"id": "drift",
		"uid": "default-dr-6ddbb",
		"source": {"gitRepository": "https://github.com/example-org/drift"},
		"provider": {"name": "docker"}
	}`
	ws, err := devpod.ParseAgentWorkspaceJSON(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ws.ID != "drift" {
		t.Errorf("ID = %q", ws.ID)
	}
	if ws.Source.GitRepository != "https://github.com/example-org/drift" {
		t.Errorf("GitRepository = %q", ws.Source.GitRepository)
	}
}

func TestParseAgentWorkspaceJSON_IgnoresUnknownFields(t *testing.T) {
	// Tolerant parsing: a future devpod adding fields shouldn't break us.
	const body = `{
		"id": "x",
		"source": {"gitRepository": "g"},
		"provider": {"name": "docker", "options": {"FOO": {"value": "bar"}}},
		"futureField": {"whatever": 42}
	}`
	ws, err := devpod.ParseAgentWorkspaceJSON(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ws.ID != "x" {
		t.Errorf("ID = %q", ws.ID)
	}
}

func TestParseAgentWorkspaceJSON_BadJSON(t *testing.T) {
	_, err := devpod.ParseAgentWorkspaceJSON(strings.NewReader("{not json"))
	if err == nil {
		t.Fatal("expected error on malformed JSON")
	}
}

func TestListAgentWorkspaces_EmptyRoot(t *testing.T) {
	got, err := devpod.ListAgentWorkspaces(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want empty, got %v", got)
	}
}

func TestListAgentWorkspaces_NilRoot(t *testing.T) {
	// Passing "" should return nil without error — lets callers pass
	// AgentContextsRoot() on a hostile system without a home dir.
	got, err := devpod.ListAgentWorkspaces("")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want empty, got %v", got)
	}
}

func TestListAgentWorkspaces_MultipleContexts(t *testing.T) {
	root := t.TempDir()
	// default context, wrapped form
	write(t, root, "default", "research", `{
		"workspaceOrigin": "/any",
		"workspace": {"id":"research","source":{"gitRepository":"g1"}}
	}`)
	// default context, another
	write(t, root, "default", "poc", `{
		"workspaceOrigin": "/any",
		"workspace": {"id":"poc","source":{"gitRepository":"g2"}}
	}`)
	// work context, bare form
	write(t, root, "work", "reports", `{"id":"reports","source":{"gitRepository":"g3"}}`)
	// corrupt file — should be skipped, not erroring out the whole list
	writeRaw(t, root, "default", "broken", "not-json")
	// directory without workspace.json — skipped silently
	if err := os.MkdirAll(filepath.Join(root, "default", "workspaces", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := devpod.ListAgentWorkspaces(root)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 entries, got %d (%v)", len(got), got)
	}
	// Build a lookup so order-independence is preserved.
	by := map[string]devpod.AgentWorkspaceEntry{}
	for _, e := range got {
		by[e.Context+"/"+e.Workspace.ID] = e
	}
	for _, key := range []string{"default/research", "default/poc", "work/reports"} {
		if _, ok := by[key]; !ok {
			t.Errorf("missing %q in %v", key, got)
		}
	}
}

func write(t *testing.T, root, ctx, wsID, body string) {
	t.Helper()
	writeRaw(t, root, ctx, wsID, body)
}

func writeRaw(t *testing.T, root, ctx, wsID, body string) {
	t.Helper()
	dir := filepath.Join(root, ctx, "workspaces", wsID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workspace.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
