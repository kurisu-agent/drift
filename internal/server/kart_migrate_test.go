package server_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kurisu-agent/drift/internal/devpod"
	driftexec "github.com/kurisu-agent/drift/internal/exec"
	"github.com/kurisu-agent/drift/internal/model"
	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/server"
	"github.com/kurisu-agent/drift/internal/wire"
)

func writeAgentWorkspace(t *testing.T, root, ctx, id, repo string) {
	t.Helper()
	dir := filepath.Join(root, ctx, "workspaces", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"workspaceOrigin":"/any","workspace":{"id":"` + id + `","source":{"gitRepository":"` + repo + `"}}}`
	if err := os.WriteFile(filepath.Join(dir, "workspace.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestKartMigrateListFiltersDriftOwned(t *testing.T) {
	t.Parallel()
	deps := newKartDeps(t, &fakeDevpod{replies: map[string]fakeReply{}})
	agentRoot := t.TempDir()

	// Three raw candidates under default/: one already adopted (drift
	// kart exists with matching name), one already migrated via back-
	// reference under a different kart name, and one fresh.
	writeAgentWorkspace(t, agentRoot, "default", "already-a-kart", "https://example.com/a.git")
	writeAgentWorkspace(t, agentRoot, "default", "renamed-after-migrate", "https://example.com/b.git")
	writeAgentWorkspace(t, agentRoot, "default", "fresh", "https://example.com/c.git")
	// Non-git workspace — should be silently dropped.
	nongitDir := filepath.Join(agentRoot, "default", "workspaces", "no-git")
	if err := os.MkdirAll(nongitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nongitDir, "workspace.json"),
		[]byte(`{"workspaceOrigin":"/any","workspace":{"id":"no-git","source":{"image":"alpine"}}}`),
		0o644); err != nil {
		t.Fatal(err)
	}

	// Garage side: already-a-kart is drift-managed (exists in karts/),
	// and a differently-named kart carries a migrated_from back-reference
	// targeting default/renamed-after-migrate.
	writeKart(t, deps, "already-a-kart", server.KartConfig{Repo: "https://example.com/a.git"})
	writeKart(t, deps, "moved-here", server.KartConfig{
		Repo:         "https://example.com/b.git",
		MigratedFrom: &model.MigratedFrom{Context: "default", Name: "renamed-after-migrate"},
	})

	reg := rpc.NewRegistry()
	server.RegisterKartMigrate(reg, server.KartMigrateDeps{KartDeps: deps, AgentRoot: agentRoot})
	resp := reg.Dispatch(t.Context(), &wire.Request{
		JSONRPC: wire.Version,
		Method:  wire.MethodKartMigrateList,
		Params:  json.RawMessage(`{}`),
		ID:      json.RawMessage("1"),
	})
	if resp.Error != nil {
		t.Fatalf("dispatch error: %+v", resp.Error)
	}
	var got server.KartMigrateListResult
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Candidates) != 1 {
		t.Fatalf("want 1 candidate, got %d: %+v", len(got.Candidates), got.Candidates)
	}
	c := got.Candidates[0]
	if c.Name != "fresh" || c.Context != "default" || c.Repo != "https://example.com/c.git" {
		t.Errorf("unexpected candidate: %+v", c)
	}
}

func TestKartMigrateListMultipleContexts(t *testing.T) {
	t.Parallel()
	deps := newKartDeps(t, &fakeDevpod{replies: map[string]fakeReply{}})
	agentRoot := t.TempDir()
	writeAgentWorkspace(t, agentRoot, "default", "alpha", "https://example.com/a.git")
	writeAgentWorkspace(t, agentRoot, "work", "alpha", "https://example.com/b.git")

	reg := rpc.NewRegistry()
	server.RegisterKartMigrate(reg, server.KartMigrateDeps{KartDeps: deps, AgentRoot: agentRoot})
	resp := reg.Dispatch(t.Context(), &wire.Request{
		JSONRPC: wire.Version,
		Method:  wire.MethodKartMigrateList,
		Params:  json.RawMessage(`{}`),
		ID:      json.RawMessage("1"),
	})
	if resp.Error != nil {
		t.Fatalf("dispatch error: %+v", resp.Error)
	}
	var got server.KartMigrateListResult
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Same name, different contexts — both should appear so the user
	// can pick which one to migrate.
	if len(got.Candidates) != 2 {
		t.Fatalf("want 2 candidates, got %d", len(got.Candidates))
	}
	byCtx := map[string]server.KartMigrateCandidate{}
	for _, c := range got.Candidates {
		byCtx[c.Context] = c
	}
	if byCtx["default"].Repo != "https://example.com/a.git" {
		t.Errorf("default/alpha.repo = %q", byCtx["default"].Repo)
	}
	if byCtx["work"].Repo != "https://example.com/b.git" {
		t.Errorf("work/alpha.repo = %q", byCtx["work"].Repo)
	}
}

func TestKartMigrateDeleteOldRefusesDriftContext(t *testing.T) {
	t.Parallel()
	deps := newKartDeps(t, &fakeDevpod{replies: map[string]fakeReply{}})
	reg := rpc.NewRegistry()
	server.RegisterKartMigrate(reg, server.KartMigrateDeps{KartDeps: deps})
	resp := reg.Dispatch(t.Context(), &wire.Request{
		JSONRPC: wire.Version,
		Method:  wire.MethodKartMigrateDeleteOld,
		Params:  json.RawMessage(`{"context":"drift","name":"whatever"}`),
		ID:      json.RawMessage("1"),
	})
	if resp.Error == nil {
		t.Fatal("want error when targeting drift context, got nil")
	}
	// Surface the typed error for specific assertion.
	got := rpcerr.FromWire(resp.Error)
	if got == nil || got.Type != rpcerr.TypeInvalidFlag {
		t.Errorf("want invalid_flag, got %+v", got)
	}
}

func TestKartMigrateDeleteOldPassesContextFlag(t *testing.T) {
	t.Parallel()
	// fakeDevpod keyed by the FIRST arg — but withContext prepends
	// --context so the subcommand is now at index 2. Detect both shapes
	// so this test doesn't silently pass through a regression.
	rec := &contextCaptureRunner{}
	deps := server.KartDeps{
		Devpod:    &devpod.Client{Binary: "devpod", Runner: rec},
		GarageDir: t.TempDir(),
	}
	if err := os.MkdirAll(filepath.Join(deps.GarageDir, "karts"), 0o755); err != nil {
		t.Fatal(err)
	}
	reg := rpc.NewRegistry()
	server.RegisterKartMigrate(reg, server.KartMigrateDeps{KartDeps: deps})
	resp := reg.Dispatch(t.Context(), &wire.Request{
		JSONRPC: wire.Version,
		Method:  wire.MethodKartMigrateDeleteOld,
		Params:  json.RawMessage(`{"context":"work","name":"alpha"}`),
		ID:      json.RawMessage("1"),
	})
	if resp.Error != nil {
		t.Fatalf("dispatch error: %+v", resp.Error)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("want 1 devpod call, got %d", len(rec.calls))
	}
	args := rec.calls[0].Args
	if len(args) < 4 || args[0] != "--context" || args[1] != "work" || args[2] != "delete" {
		t.Errorf("unexpected argv: %v (want --context work delete --force alpha)", args)
	}
}

type contextCaptureRunner struct {
	calls []driftexec.Cmd
}

func (r *contextCaptureRunner) Run(_ context.Context, cmd driftexec.Cmd) (driftexec.Result, error) {
	r.calls = append(r.calls, cmd)
	return driftexec.Result{}, nil
}
