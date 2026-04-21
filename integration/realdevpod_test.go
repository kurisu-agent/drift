//go:build integration

package integration_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/kurisu-agent/drift/integration"
	"github.com/kurisu-agent/drift/internal/wire"
)

// TestRealDevpodUpAndDelete is the full-chain smoke: no recorder shim, real
// devpod v0.22.0 inside the circuit actually builds a workspace against the
// host's docker daemon (socket bind-mounted from the devcontainer). After
// `drift new` succeeds we assert the kart reaches running state, then let
// `drift delete` tear it down and check the host has no stray workspace
// container for this test's name prefix.
//
// Slower than the shim-based tests (~30-60s) because devpod pulls an image
// and bootstraps its in-container agent. Skipped under -short so a quick
// `make integration -short` stays fast.
func TestRealDevpodUpAndDelete(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-devpod E2E in -short mode")
	}

	ctx := integration.TestCtx(t, 6*time.Minute)

	c, _ := integration.StartReadyCircuit(ctx, t, false)
	// `lakitu init` already auto-registers the docker provider idempotently
	// via devpod.EnsureProvider, so no follow-up `provider add` is needed.

	// Minimal starter with a devcontainer.json pointing at debian:bookworm-slim
	// — already cached on the host (the circuit image uses the same base), so
	// no pull during the test. bash + coreutils are present so devpod's agent
	// can bootstrap.
	starterURL := c.StageStarter(ctx, "real-starter", map[string]string{
		"README.md":                       "# real-devpod starter\n",
		".devcontainer/devcontainer.json": `{"image":"debian:bookworm-slim"}` + "\n",
	})

	kart := c.KartName("real")

	// Baseline: snapshot the dev.containers.id population BEFORE drift new.
	// Devpod tags every devcontainer it builds with that label, so the
	// assertion can watch the population grow (on new) and shrink (on
	// delete) regardless of the auto-generated container names.
	baseline := integration.DevcontainerIDs(ctx, t)

	stdout, stderr, code := c.Drift(ctx, "new", kart,
		"--tune", "none",
		"--starter", starterURL,
	)
	if code != 0 {
		t.Fatalf("drift new: code=%d\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}

	// kart.info must report running — the drift CLI has no info surface,
	// so drive lakitu directly.
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

	// New workspace containers should now exist on the host.
	afterUp := integration.DevcontainerIDs(ctx, t)
	created := integration.SetDiff(afterUp, baseline)
	if len(created) == 0 {
		t.Fatalf("no new devcontainer on host after drift new; baseline=%v after=%v", baseline, afterUp)
	}

	// Delete via drift — devpod should tear down every workspace container
	// that drift new brought up, returning the population to baseline.
	// `-y` is required on non-TTY stdin so the destructive prompt doesn't
	// block the test harness.
	_, stderr, code = c.Drift(ctx, "delete", "-y", kart)
	if code != 0 {
		t.Fatalf("drift delete: code=%d stderr=%q", code, stderr)
	}
	afterDelete := integration.DevcontainerIDs(ctx, t)
	if orphans := integration.SetDiff(afterDelete, baseline); len(orphans) > 0 {
		t.Errorf("devcontainer orphans after drift delete: %v", orphans)
	}
}
