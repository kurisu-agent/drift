//go:build integration

package integration_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kurisu-agent/drift/integration"
	"github.com/kurisu-agent/drift/internal/wire"
)

// TestRealDevpodSeedClaudeCode is the end-to-end smoke for the
// `seed: [claudeCode]` tune opt-in. After `drift new` brings up a real
// workspace via devpod, the post-`devpod up` finaliser must drop two
// files into the kart container's $HOME:
//
//   - $HOME/.claude/CLAUDE.md — orientation blurb pointing at the
//     project's devcontainer.json under /workspaces/<kart>/.
//   - $HOME/.claude.json — minimal profile with hasCompletedOnboarding
//     and a per-project hasTrustDialogAccepted entry keyed by the
//     workspace path. Skips Claude Code's first-run login picker and
//     trust prompt when the user later runs `claude` inside the kart.
//
// Slower than the recorder-shim tune tests because devpod actually
// builds a workspace container; skipped under -short.
func TestRealDevpodSeedClaudeCode(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-devpod seed E2E in -short mode")
	}

	ctx := integration.TestCtx(t, 6*time.Minute)

	c, _ := integration.StartReadyCircuit(ctx, t, false)

	// Same minimal starter shape as TestRealDevpodUpAndDelete — the image
	// is already cached on the host so no pull during the test.
	starterURL := c.StageStarter(ctx, "seed-starter", map[string]string{
		"README.md":                       "# seed starter\n",
		".devcontainer/devcontainer.json": `{"image":"debian:bookworm-slim"}` + "\n",
	})

	if _, err := c.LakituRPC(ctx, wire.MethodTuneNew, map[string]any{
		"name":    "seededtune",
		"starter": starterURL,
		"seed":    []string{"claudeCode"},
	}); err != nil {
		t.Fatalf("tune.new: %v", err)
	}

	kart := c.KartName("seed")
	baseline := integration.DevcontainerIDs(ctx, t)

	stdout, stderr, code := c.Drift(ctx, "new", kart, "--tune", "seededtune")
	if code != 0 {
		t.Fatalf("drift new: code=%d\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}
	t.Cleanup(func() {
		// Best-effort teardown so a failure mid-test doesn't leak a
		// workspace container; the assertion-level deletes below also
		// cover the happy path.
		_, _, _ = c.Drift(ctx, "kart", "delete", "-y", kart)
	})

	infoRaw, err := c.LakituRPC(ctx, wire.MethodKartInfo, map[string]string{"name": kart})
	if err != nil {
		t.Fatalf("kart.info: %v", err)
	}
	var info struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(infoRaw, &info); err != nil {
		t.Fatalf("decode kart.info: %v\nraw=%s", err, infoRaw)
	}
	if info.Status != "running" {
		t.Fatalf("kart.info status = %q, want running", info.Status)
	}

	afterUp := integration.DevcontainerIDs(ctx, t)
	var wcID string
	for id := range integration.SetDiffSet(afterUp, baseline) {
		wcID = integration.WorkspaceContainerName(ctx, t, id)
		if wcID != "" {
			break
		}
	}
	if wcID == "" {
		t.Fatalf("could not resolve workspace container; baseline=%v after=%v", baseline, afterUp)
	}

	// CLAUDE.md: orientation blurb anchored at the project's
	// devcontainer.json under the kart's /workspaces/<name>/. The image
	// portion of the blurb is probe-best-effort and only lands for clone
	// karts (probe reads from devpod's agent-context content/) — not
	// asserted here so a probe regression doesn't masquerade as a seed
	// regression.
	mdRaw, err := integration.DockerExec(ctx, wcID, "sh", "-c", `cat "$HOME/.claude/CLAUDE.md"`)
	if err != nil {
		t.Fatalf("read CLAUDE.md: %v", err)
	}
	md := string(mdRaw)
	wantPath := "/workspaces/" + kart + "/.devcontainer/devcontainer.json"
	for _, want := range []string{
		"You are inside a devcontainer",
		wantPath,
	} {
		if !strings.Contains(md, want) {
			t.Errorf("CLAUDE.md missing %q:\n%s", want, md)
		}
	}

	// claude.json: hasCompletedOnboarding kills the login picker;
	// projects[<workspace>].hasTrustDialogAccepted kills the per-folder
	// trust prompt.
	jsonRaw, err := integration.DockerExec(ctx, wcID, "sh", "-c", `cat "$HOME/.claude.json"`)
	if err != nil {
		t.Fatalf("read .claude.json: %v", err)
	}
	var profile struct {
		HasCompletedOnboarding bool `json:"hasCompletedOnboarding"`
		Projects               map[string]struct {
			HasTrustDialogAccepted        bool `json:"hasTrustDialogAccepted"`
			HasCompletedProjectOnboarding bool `json:"hasCompletedProjectOnboarding"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(jsonRaw, &profile); err != nil {
		t.Fatalf("parse .claude.json: %v\n%s", err, jsonRaw)
	}
	if !profile.HasCompletedOnboarding {
		t.Errorf("hasCompletedOnboarding = false; want true\n%s", jsonRaw)
	}
	wsKey := "/workspaces/" + kart
	proj, ok := profile.Projects[wsKey]
	if !ok {
		t.Fatalf("projects[%q] missing; got keys %v\n%s", wsKey, mapKeys(profile.Projects), jsonRaw)
	}
	if !proj.HasTrustDialogAccepted {
		t.Errorf("projects[%q].hasTrustDialogAccepted = false; want true", wsKey)
	}
	if !proj.HasCompletedProjectOnboarding {
		t.Errorf("projects[%q].hasCompletedProjectOnboarding = false; want true", wsKey)
	}

	// Re-running the only-if-absent path: rewrite ~/.claude.json with a
	// sentinel marker, then delete + recreate the kart. The sentinel
	// would only survive if the seed code were appending or overwriting,
	// which the OnlyIfAbsent flag is supposed to prevent. Skipped here
	// because the kart finaliser only runs at create time, not on
	// restart — exercising it would mean a second `drift new` against a
	// clean workspace, doubling the test's wallclock for low marginal
	// signal. The unit test in internal/kart/seed_fragment_test.go
	// covers the shell shape directly.

	if _, _, code := c.Drift(ctx, "kart", "delete", "-y", kart); code != 0 {
		t.Errorf("drift kart delete: code=%d", code)
	}
}

func mapKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
