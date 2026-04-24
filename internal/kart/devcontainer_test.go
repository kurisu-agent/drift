package kart

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kurisu-agent/drift/internal/model"
	"github.com/kurisu-agent/drift/internal/rpcerr"
)

func TestNormalizeDevcontainerEmpty(t *testing.T) {
	p, cleanup, err := NormalizeDevcontainer(context.Background(), "", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if p != "" {
		t.Fatalf("expected empty path, got %q", p)
	}
}

func TestNormalizeDevcontainerFilePath(t *testing.T) {
	p, cleanup, err := NormalizeDevcontainer(context.Background(), "/etc/passwd", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if p != "/etc/passwd" {
		t.Fatalf("expected passthrough, got %q", p)
	}
}

func TestNormalizeDevcontainerJSON(t *testing.T) {
	dir := t.TempDir()
	p, cleanup, err := NormalizeDevcontainer(context.Background(), `{"image":"ubuntu"}`, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if filepath.Dir(p) != dir {
		t.Fatalf("expected file under %s, got %s", dir, p)
	}
	buf, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(buf), "ubuntu") {
		t.Fatalf("unexpected body: %s", buf)
	}
}

func TestNormalizeDevcontainerJSONInvalid(t *testing.T) {
	_, _, err := NormalizeDevcontainer(context.Background(), `{broken`, t.TempDir(), nil)
	var re *rpcerr.Error
	if !errors.As(err, &re) || re.Type != rpcerr.TypeInvalidFlag {
		t.Fatalf("expected invalid_flag, got %v", err)
	}
}

func TestNormalizeDevcontainerURL(t *testing.T) {
	dir := t.TempDir()
	fake := DevcontainerFetcher(func(ctx context.Context, url string) ([]byte, error) {
		if url != "https://example.com/dc.json" {
			t.Fatalf("unexpected URL %q", url)
		}
		return []byte(`{"image":"debian"}`), nil
	})
	p, cleanup, err := NormalizeDevcontainer(context.Background(), "https://example.com/dc.json", dir, fake)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	buf, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(buf), "debian") {
		t.Fatalf("unexpected body: %s", buf)
	}
}

// TestNormalizeDevcontainerWithMountsOnly covers the "no base devcontainer,
// just mount_dirs" path — the overlay should be a standalone {"mounts":...}
// file that devpod's mergeMounts unions with the project's devcontainer.json.
func TestNormalizeDevcontainerWithMountsOnly(t *testing.T) {
	dir := t.TempDir()
	mounts := []model.Mount{
		{Type: "bind", Source: "/home/dev/.claude", Target: "/home/dev/.claude"},
	}
	p, cleanup, err := NormalizeDevcontainerWithOverlay(context.Background(), "", dir, Overlay{Mounts: mounts}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	buf, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf, &got); err != nil {
		t.Fatalf("overlay not valid JSON: %v\n%s", err, buf)
	}
	gotMounts, _ := got["mounts"].([]any)
	if len(gotMounts) != 1 {
		t.Fatalf("expected 1 mount, got %d: %s", len(gotMounts), buf)
	}
	first := gotMounts[0].(map[string]any)
	if first["target"] != "/home/dev/.claude" || first["type"] != "bind" {
		t.Fatalf("unexpected mount: %v", first)
	}
}

// TestNormalizeDevcontainerWithMountsSplicesJSONC proves that a user's
// devcontainer source with comments + trailing commas is parsed via hujson,
// spliced, and serialized as strict JSON (so devpod can re-parse it).
func TestNormalizeDevcontainerWithMountsSplicesJSONC(t *testing.T) {
	dir := t.TempDir()
	base := "{\n" +
		"  // a comment\n" +
		"  \"image\": \"ubuntu\",\n" +
		"  \"mounts\": [\n" +
		"    {\"type\": \"bind\", \"source\": \"/tmp/existing\", \"target\": \"/existing\"},\n" +
		"  ],\n" +
		"}\n"
	basePath := filepath.Join(dir, "input-devcontainer.json")
	if err := os.WriteFile(basePath, []byte(base), 0o600); err != nil {
		t.Fatal(err)
	}
	mounts := []model.Mount{
		{Type: "bind", Source: "/opt/shared", Target: "/opt/shared"},
	}
	p, cleanup, err := NormalizeDevcontainerWithOverlay(context.Background(), basePath, dir, Overlay{Mounts: mounts}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	buf, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf, &got); err != nil {
		t.Fatalf("overlay not strict JSON: %v\n%s", err, buf)
	}
	if got["image"] != "ubuntu" {
		t.Fatalf("image key lost: %v", got)
	}
	gotMounts, _ := got["mounts"].([]any)
	if len(gotMounts) != 2 {
		t.Fatalf("expected 2 mounts (1 kept + 1 new), got %d: %s", len(gotMounts), buf)
	}
	// Order: existing kept first, new appended.
	targets := []string{
		gotMounts[0].(map[string]any)["target"].(string),
		gotMounts[1].(map[string]any)["target"].(string),
	}
	if targets[0] != "/existing" || targets[1] != "/opt/shared" {
		t.Fatalf("unexpected mount order: %v", targets)
	}
}

// TestSpliceOverlayMountsDedupOnTarget proves local dedup: a splice-time
// mount whose target collides with an existing base mount REPLACES the
// base one. (devpod's own mergeMounts then unions against the project
// file.)
func TestSpliceOverlayMountsDedupOnTarget(t *testing.T) {
	base := []byte(`{"mounts":[{"type":"bind","source":"/old","target":"/same"}]}`)
	mounts := []model.Mount{
		{Type: "bind", Source: "/new", Target: "/same"},
	}
	out, err := spliceOverlay(base, Overlay{Mounts: mounts})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	gotMounts := got["mounts"].([]any)
	if len(gotMounts) != 1 {
		t.Fatalf("expected 1 mount after dedup, got %d", len(gotMounts))
	}
	only := gotMounts[0].(map[string]any)
	if only["source"] != "/new" {
		t.Fatalf("splice-time mount did not win: %v", only)
	}
}

// TestSpliceOverlayUserNormalisation covers remoteUser + onCreateCommand
// splicing and the preservation of a project-authored onCreateCommand
// under the "project" key.
func TestSpliceOverlayUserNormalisation(t *testing.T) {
	base := []byte(`{"image":"ubuntu","onCreateCommand":"echo project"}`)
	out, err := spliceOverlay(base, Overlay{NormaliseUser: true, Character: "kurisu"})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if got["remoteUser"] != "kurisu" {
		t.Fatalf("remoteUser not set: %v", got["remoteUser"])
	}
	cmd, ok := got["onCreateCommand"].(map[string]any)
	if !ok {
		t.Fatalf("onCreateCommand not an object: %T", got["onCreateCommand"])
	}
	if _, ok := cmd[normaliseUserOnCreateKey]; !ok {
		t.Fatalf("lakitu onCreateCommand key missing: %v", cmd)
	}
	if cmd["project"] != "echo project" {
		t.Fatalf("project onCreateCommand not preserved: %v", cmd["project"])
	}
}

// TestSpliceOverlayUserNormalisationIdempotent re-runs the splice over
// an already-spliced overlay and proves our key doesn't accumulate.
func TestSpliceOverlayUserNormalisationIdempotent(t *testing.T) {
	base := []byte(`{"image":"ubuntu"}`)
	once, err := spliceOverlay(base, Overlay{NormaliseUser: true, Character: "kurisu"})
	if err != nil {
		t.Fatal(err)
	}
	twice, err := spliceOverlay(once, Overlay{NormaliseUser: true, Character: "kurisu"})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(twice, &got); err != nil {
		t.Fatal(err)
	}
	cmd := got["onCreateCommand"].(map[string]any)
	if len(cmd) != 1 {
		t.Fatalf("expected 1 onCreateCommand entry after re-splice, got %d: %v", len(cmd), cmd)
	}
}
