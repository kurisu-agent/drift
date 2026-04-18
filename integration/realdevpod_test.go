//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
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

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	c := integration.StartCircuit(ctx, t)
	if err := integration.SSHCommand(ctx, c, "lakitu", "init"); err != nil {
		t.Fatalf("lakitu init: %v", err)
	}
	c.RegisterCircuit(ctx, "test")
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
	baseline := devcontainerIDs(ctx, t)

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
	afterUp := devcontainerIDs(ctx, t)
	created := setDiff(afterUp, baseline)
	if len(created) == 0 {
		t.Fatalf("no new devcontainer on host after drift new; baseline=%v after=%v", baseline, afterUp)
	}

	// Delete via drift — devpod should tear down every workspace container
	// that drift new brought up, returning the population to baseline.
	_, stderr, code = c.Drift(ctx, "delete", kart)
	if code != 0 {
		t.Fatalf("drift delete: code=%d stderr=%q", code, stderr)
	}
	afterDelete := devcontainerIDs(ctx, t)
	if orphans := setDiff(afterDelete, baseline); len(orphans) > 0 {
		t.Errorf("devcontainer orphans after drift delete: %v", orphans)
	}
}

// devcontainerIDs returns the unique dev.containers.id values present on the
// outer docker daemon — one per devpod-managed workspace regardless of the
// generated container name.
func devcontainerIDs(ctx context.Context, t *testing.T) map[string]struct{} {
	t.Helper()
	out, err := exec.CommandContext(ctx, "docker", "ps", "-a",
		"--filter", "label=dev.containers.id",
		"--format", "{{.Label \"dev.containers.id\"}}").Output()
	if err != nil {
		t.Fatalf("docker ps dev.containers.id: %v", err)
	}
	ids := map[string]struct{}{}
	for _, id := range strings.Fields(strings.TrimSpace(string(out))) {
		ids[id] = struct{}{}
	}
	return ids
}

// setDiff returns the members of a not present in b.
func setDiff(a, b map[string]struct{}) []string {
	var out []string
	for k := range a {
		if _, ok := b[k]; !ok {
			out = append(out, k)
		}
	}
	return out
}
