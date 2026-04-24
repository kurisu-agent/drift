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

// TestSpliceOverlayRewritesHomeTarget covers the `~/X` → /mnt/lakitu-host/X
// rewrite that happens at splice time only. resolved.Mounts keeps the
// `~/` form (so drift-detection compares apples to apples with the
// tune spec) and the post-up LinkHostMountsIntoHome helper symlinks
// $HOME/X to the mounted path.
func TestSpliceOverlayRewritesHomeTarget(t *testing.T) {
	base := []byte(`{"image":"ubuntu"}`)
	mounts := []model.Mount{
		{Type: "bind", Source: "/home/dev/.claude", Target: "~/.claude"},
		{Type: "bind", Source: "/opt/shared", Target: "/opt/shared"},
	}
	out, err := spliceOverlay(base, Overlay{Mounts: mounts})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	gotMounts := got["mounts"].([]any)
	if len(gotMounts) != 2 {
		t.Fatalf("expected 2 mounts, got %d: %s", len(gotMounts), out)
	}
	if gotMounts[0].(map[string]any)["target"] != "/mnt/lakitu-host/.claude" {
		t.Fatalf("~/ target not rewritten: %v", gotMounts[0])
	}
	if gotMounts[1].(map[string]any)["target"] != "/opt/shared" {
		t.Fatalf("absolute target should pass through: %v", gotMounts[1])
	}
}

// TestSpliceOverlayExpandsSourceTilde — the overlay splicer is the
// point where source-side `~/` becomes an absolute host path (so
// devpod hands a real directory to docker). resolved.Mounts (and
// KartConfig.mount_dirs) keep the tilde form for drift-detection
// parity with the tune spec.
func TestSpliceOverlayExpandsSourceTilde(t *testing.T) {
	t.Setenv("HOME", "/home/circuit-user")
	base := []byte(`{"image":"ubuntu"}`)
	mounts := []model.Mount{
		{Type: "bind", Source: "~/.claude", Target: "~/.claude"},
	}
	out, err := spliceOverlay(base, Overlay{Mounts: mounts})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	m := got["mounts"].([]any)[0].(map[string]any)
	if m["source"] != "/home/circuit-user/.claude" {
		t.Fatalf("splice-time source expansion: got %v", m["source"])
	}
	if m["target"] != "/mnt/lakitu-host/.claude" {
		t.Fatalf("splice-time target rewrite: got %v", m["target"])
	}
}

func TestTargetInHome(t *testing.T) {
	cases := []struct {
		in     string
		ok     bool
		suffix string
	}{
		{"~/.claude", true, ".claude"},
		{"~/a/b", true, "a/b"},
		{"~", true, ""},
		{"/abs/path", false, ""},
		{"./rel", false, ""},
		{"", false, ""},
	}
	for _, c := range cases {
		suffix, ok := targetInHome(c.in)
		if ok != c.ok || suffix != c.suffix {
			t.Errorf("targetInHome(%q) = (%q, %v), want (%q, %v)", c.in, suffix, ok, c.suffix, c.ok)
		}
	}
}
