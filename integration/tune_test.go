//go:build integration

package integration_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kurisu-agent/drift/integration"
	"github.com/kurisu-agent/drift/internal/wire"
)

// TestTuneStarter verifies the starter tune field composes into kart.new:
// `drift new --tune <mytune>` should clone the tune's starter URL, strip
// history, and pass the resulting local path to `devpod up` as the
// positional source argument via --id.
func TestTuneStarter(t *testing.T) {
	ctx := integration.TestCtx(t, 5*time.Minute)

	c, rec := integration.StartReadyCircuit(ctx, t, true)
	starterURL := c.StageStarter(ctx, "starterA", map[string]string{"README.md": "# starter\n"})

	if _, err := c.LakituRPC(ctx, wire.MethodTuneNew, map[string]string{
		"name":    "mytune",
		"starter": starterURL,
	}); err != nil {
		t.Fatalf("tune.new: %v", err)
	}

	kart := c.KartName("starter")
	if _, stderr, code := c.Drift(ctx, "new", kart, "--tune", "mytune"); code != 0 {
		t.Fatalf("drift new: code=%d stderr=%q", code, stderr)
	}

	up := rec.FindUp(ctx)
	if up == nil {
		t.Fatalf("no devpod up invocation recorded")
	}
	// Starter mode: devpod up gets `--id <kart> <source-dir>` tail. The
	// source dir is the drift scratch tmpdir — path starts with /tmp.
	id := integration.ArgvValue(up.Argv, "--id")
	if id != kart {
		t.Errorf("--id = %q, want %q", id, kart)
	}
	last := up.Argv[len(up.Argv)-1]
	if !strings.HasPrefix(last, "/tmp/") || !strings.Contains(last, "drift-kart-"+kart) {
		t.Errorf("source arg = %q, want /tmp/drift-kart-%s-…", last, kart)
	}

	// Artifact check: the shim preserved a copy of the source dir so the
	// test can verify history was stripped (starter git clone → rm .git →
	// git init → single initial commit). README.md from the staged starter
	// must also be present.
	files := c.ListArtifact(ctx, up, "source")
	hasREADME, hasGit := false, false
	for _, f := range files {
		switch f {
		case "README.md":
			hasREADME = true
		case ".git":
			hasGit = true
		}
	}
	if !hasREADME {
		t.Errorf("source artifact missing README.md, got %v", files)
	}
	if !hasGit {
		t.Errorf("source artifact missing .git dir (starter strip re-inits): %v", files)
	}
	// Verify the re-init produced exactly one commit with the drift
	// fallback author (we didn't attach a character to this kart).
	logOut := strings.TrimSpace(string(c.ExecInContainer(ctx,
		"git", "-C", filepath.Join(up.ArtifactDir, "source"),
		"log", "--format=%an <%ae>%n%s")))
	lines := strings.Split(logOut, "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 git-log lines (author, subject), got %d:\n%s", len(lines), logOut)
	}
	if lines[0] != "drift <noreply@drift.local>" {
		t.Errorf("starter commit author = %q, want drift fallback", lines[0])
	}
	if !strings.HasPrefix(lines[1], "Initial commit from starter") {
		t.Errorf("starter commit subject = %q, want 'Initial commit from starter…'", lines[1])
	}
}

// TestTuneDevcontainer: `devcontainer` field on a tune should land as
// `--extra-devcontainer-path` on devpod up. The tune writer accepts a file
// path / JSON / URL — a file path is the simplest case and exercises the
// passthrough path of kart.NormalizeDevcontainer.
func TestTuneDevcontainer(t *testing.T) {
	ctx := integration.TestCtx(t, 5*time.Minute)

	c, rec := integration.StartReadyCircuit(ctx, t, true)

	// Stage a tiny devcontainer.json file inside the circuit so the
	// passthrough path has a real file to reference.
	dcPath := "/tmp/tune-devcontainer.json"
	if err := integration.SSHCommand(ctx, c, "sh", "-c",
		`printf '%s' '{"image":"alpine:latest"}' > `+dcPath); err != nil {
		t.Fatalf("stage devcontainer: %v", err)
	}

	if _, err := c.LakituRPC(ctx, wire.MethodTuneNew, map[string]string{
		"name":         "dctune",
		"devcontainer": dcPath,
	}); err != nil {
		t.Fatalf("tune.new: %v", err)
	}

	kart := c.KartName("dc")
	if _, stderr, code := c.Drift(ctx, "new", kart, "--tune", "dctune"); code != 0 {
		t.Fatalf("drift new: code=%d stderr=%q", code, stderr)
	}

	up := rec.FindUp(ctx)
	if up == nil {
		t.Fatalf("no devpod up invocation recorded")
	}
	got := integration.ArgvValue(up.Argv, "--extra-devcontainer-path")
	if got != dcPath {
		t.Errorf("--extra-devcontainer-path = %q, want %q", got, dcPath)
	}

	// Artifact check: the shim copied the file devpod was pointed at. We
	// staged it as a literal alpine devcontainer — the bytes should match.
	body := c.ReadArtifact(ctx, up, "extra-devcontainer.json")
	if err := assertJSONEqual(string(body), `{"image":"alpine:latest"}`); err != nil {
		t.Errorf("devcontainer artifact content: %v", err)
	}
}

// TestTuneMountDirs: a tune with `mount_dirs` produces an
// --extra-devcontainer-path overlay whose JSON carries a `mounts` array
// with the full devcontainer spec. Covers the "no base devcontainer" case
// where drift synthesizes a standalone overlay.
func TestTuneMountDirs(t *testing.T) {
	ctx := integration.TestCtx(t, 5*time.Minute)

	c, rec := integration.StartReadyCircuit(ctx, t, true)

	if _, err := c.LakituRPC(ctx, wire.MethodTuneNew, map[string]any{
		"name": "mnttune",
		"mount_dirs": []map[string]any{
			{"type": "bind", "source": "${localEnv:HOME}/.claude", "target": "/home/dev/.claude"},
		},
	}); err != nil {
		t.Fatalf("tune.new: %v", err)
	}

	kart := c.KartName("mnt")
	if _, stderr, code := c.Drift(ctx, "new", kart, "--tune", "mnttune"); code != 0 {
		t.Fatalf("drift new: code=%d stderr=%q", code, stderr)
	}

	up := rec.FindUp(ctx)
	if up == nil {
		t.Fatalf("no devpod up invocation recorded")
	}
	if got := integration.ArgvValue(up.Argv, "--extra-devcontainer-path"); got == "" {
		t.Fatalf("--extra-devcontainer-path not passed; argv=%v", up.Argv)
	}

	body := c.ReadArtifact(ctx, up, "extra-devcontainer.json")
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("overlay not JSON: %v\n%s", err, body)
	}
	mounts, _ := got["mounts"].([]any)
	if len(mounts) != 1 {
		t.Fatalf("expected 1 mount in overlay, got %d: %s", len(mounts), body)
	}
	m, _ := mounts[0].(map[string]any)
	if m["target"] != "/home/dev/.claude" || m["type"] != "bind" {
		t.Fatalf("unexpected mount: %v", m)
	}
	if src, _ := m["source"].(string); !strings.Contains(src, "${localEnv:HOME}") {
		t.Fatalf("source lost localEnv substitution marker: %q", src)
	}
}

// TestMountFlagAppendsToTune: `--mount` on `drift new` appends to the
// tune's mount_dirs; flag-wins on target collisions via the resolver's
// last-write dedup.
func TestMountFlagAppendsToTune(t *testing.T) {
	ctx := integration.TestCtx(t, 5*time.Minute)

	c, rec := integration.StartReadyCircuit(ctx, t, true)

	if _, err := c.LakituRPC(ctx, wire.MethodTuneNew, map[string]any{
		"name": "flagmnttune",
		"mount_dirs": []map[string]any{
			{"type": "bind", "source": "/circuit/tune", "target": "/tune-only"},
		},
	}); err != nil {
		t.Fatalf("tune.new: %v", err)
	}

	kart := c.KartName("mntflag")
	if _, stderr, code := c.Drift(ctx, "new", kart,
		"--tune", "flagmnttune",
		"--mount", "type=bind,source=/circuit/flag,target=/flag-only",
	); code != 0 {
		t.Fatalf("drift new: code=%d stderr=%q", code, stderr)
	}

	up := rec.FindUp(ctx)
	if up == nil {
		t.Fatalf("no devpod up invocation recorded")
	}
	body := c.ReadArtifact(ctx, up, "extra-devcontainer.json")
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("overlay not JSON: %v\n%s", err, body)
	}
	mounts, _ := got["mounts"].([]any)
	if len(mounts) != 2 {
		t.Fatalf("expected 2 mounts in overlay, got %d: %s", len(mounts), body)
	}
	targets := make([]string, len(mounts))
	for i, raw := range mounts {
		m := raw.(map[string]any)
		targets[i] = m["target"].(string)
	}
	// Tune's mount first, flag's appended after.
	if targets[0] != "/tune-only" || targets[1] != "/flag-only" {
		t.Fatalf("unexpected target order: %v", targets)
	}
}

// TestTuneDotfilesRepo: the tune's dotfiles_repo lands as `--dotfiles` on
// devpod up (layer-2 dotfiles — layer-1 is handled separately via
// install-dotfiles after up).
func TestTuneDotfilesRepo(t *testing.T) {
	ctx := integration.TestCtx(t, 5*time.Minute)

	c, rec := integration.StartReadyCircuit(ctx, t, true)

	const dotfilesURL = "https://example.com/my/dotfiles"
	if _, err := c.LakituRPC(ctx, wire.MethodTuneNew, map[string]string{
		"name":          "dftune",
		"dotfiles_repo": dotfilesURL,
	}); err != nil {
		t.Fatalf("tune.new: %v", err)
	}

	kart := c.KartName("df")
	if _, stderr, code := c.Drift(ctx, "new", kart, "--tune", "dftune"); code != 0 {
		t.Fatalf("drift new: code=%d stderr=%q", code, stderr)
	}

	up := rec.FindUp(ctx)
	if up == nil {
		t.Fatalf("no devpod up invocation recorded")
	}
	got := integration.ArgvValue(up.Argv, "--dotfiles")
	if got != dotfilesURL {
		t.Errorf("--dotfiles = %q, want %q", got, dotfilesURL)
	}
}

// TestTuneFeatures: the tune's features JSON lands verbatim as
// `--additional-features` on devpod up when no explicit --features flag is
// given. This is the flag the skevetter fork added (the upstream devpod in
// production doesn't accept --additional-features, which is why the fork
// is pinned here).
func TestTuneFeatures(t *testing.T) {
	ctx := integration.TestCtx(t, 5*time.Minute)

	c, rec := integration.StartReadyCircuit(ctx, t, true)

	const tuneFeatures = `{"ghcr.io/devcontainers/features/node:1":{"version":"20"}}`
	if _, err := c.LakituRPC(ctx, wire.MethodTuneNew, map[string]string{
		"name":     "ftune",
		"features": tuneFeatures,
	}); err != nil {
		t.Fatalf("tune.new: %v", err)
	}

	kart := c.KartName("feat")
	if _, stderr, code := c.Drift(ctx, "new", kart, "--tune", "ftune"); code != 0 {
		t.Fatalf("drift new: code=%d stderr=%q", code, stderr)
	}

	up := rec.FindUp(ctx)
	if up == nil {
		t.Fatalf("no devpod up invocation recorded")
	}
	got := integration.ArgvValue(up.Argv, "--additional-features")
	if err := assertJSONEqual(got, tuneFeatures); err != nil {
		t.Errorf("--additional-features: %v", err)
	}
}

// TestFeaturesFlagExplicit: `drift new --features …` without a tune should
// forward the exact JSON to devpod via --additional-features. Covers the
// minimum path the fork's --additional-features support requires.
func TestFeaturesFlagExplicit(t *testing.T) {
	ctx := integration.TestCtx(t, 5*time.Minute)

	c, rec := integration.StartReadyCircuit(ctx, t, true)

	const flagFeatures = `{"ghcr.io/devcontainers/features/go:1":{"version":"1.22"}}`
	kart := c.KartName("feat-explicit")
	// --tune none opts out of the server default_tune ("default"), which
	// doesn't exist on a bare garage. The test is about --features alone
	// so the tune layer is irrelevant.
	if _, stderr, code := c.Drift(ctx, "new", kart,
		"--tune", "none",
		"--features", flagFeatures,
	); code != 0 {
		t.Fatalf("drift new: code=%d stderr=%q", code, stderr)
	}

	up := rec.FindUp(ctx)
	if up == nil {
		t.Fatalf("no devpod up invocation recorded")
	}
	got := integration.ArgvValue(up.Argv, "--additional-features")
	if err := assertJSONEqual(got, flagFeatures); err != nil {
		t.Errorf("--additional-features: %v", err)
	}
}

// TestFeaturesAdditiveMerge exercises flag composition's
// "--features is always additive": a tune with feature A and a kart invoked
// with --features B should produce a merged JSON object with both keys, and
// a shared key from --features wins over the tune's value.
func TestFeaturesAdditiveMerge(t *testing.T) {
	ctx := integration.TestCtx(t, 5*time.Minute)

	c, rec := integration.StartReadyCircuit(ctx, t, true)

	const (
		tuneFeatures = `{"ghcr.io/devcontainers/features/node:1":{"version":"20"},"ghcr.io/devcontainers/features/git:1":{}}`
		flagFeatures = `{"ghcr.io/devcontainers/features/node:1":{"version":"22"},"ghcr.io/devcontainers/features/go:1":{"version":"1.22"}}`
	)
	if _, err := c.LakituRPC(ctx, wire.MethodTuneNew, map[string]string{
		"name":     "mergetune",
		"features": tuneFeatures,
	}); err != nil {
		t.Fatalf("tune.new: %v", err)
	}

	kart := c.KartName("merge")
	if _, stderr, code := c.Drift(ctx, "new", kart,
		"--tune", "mergetune",
		"--features", flagFeatures,
	); code != 0 {
		t.Fatalf("drift new: code=%d stderr=%q", code, stderr)
	}

	up := rec.FindUp(ctx)
	if up == nil {
		t.Fatalf("no devpod up invocation recorded")
	}
	got := integration.ArgvValue(up.Argv, "--additional-features")
	wantMerged := map[string]any{
		"ghcr.io/devcontainers/features/node:1": map[string]any{"version": "22"},   // flag wins
		"ghcr.io/devcontainers/features/git:1":  map[string]any{},                  // from tune
		"ghcr.io/devcontainers/features/go:1":   map[string]any{"version": "1.22"}, // from flag
	}
	wantBytes, _ := json.Marshal(wantMerged)
	if err := assertJSONEqual(got, string(wantBytes)); err != nil {
		t.Errorf("merged features: %v", err)
	}
}

// assertJSONEqual compares two JSON strings structurally so ordering of
// object keys doesn't cause false failures. Returns nil on a match.
func assertJSONEqual(got, want string) error {
	var gotV, wantV any
	if err := json.Unmarshal([]byte(got), &gotV); err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(want), &wantV); err != nil {
		return err
	}
	gotB, _ := json.Marshal(gotV)
	wantB, _ := json.Marshal(wantV)
	if string(gotB) != string(wantB) {
		return &jsonMismatch{got: string(gotB), want: string(wantB)}
	}
	return nil
}

type jsonMismatch struct{ got, want string }

func (e *jsonMismatch) Error() string {
	return "json mismatch\n got  = " + e.got + "\n want = " + e.want
}
