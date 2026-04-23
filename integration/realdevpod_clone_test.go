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

// TestRealDevpodCloneAndDelete is the --clone counterpart to
// [TestRealDevpodUpAndDelete]. It stages a bare git repo on the circuit's
// shared scratch path (bind-mounted at the same path on the devcontainer so
// the outer dockerd can resolve it too), runs `drift new <kart> --clone
// file://…` against real devpod, and verifies:
//
//   - the kart reaches `running`
//   - the workspace container has git on PATH (the whole point of --clone
//     vs. --starter: history is preserved, so git must be usable at runtime)
//   - the cloned repo's .git/ directory lands in the workspace
//
// The devcontainer uses mcr.microsoft.com/devcontainers/base:debian because
// it ships git preinstalled; debian:bookworm-slim (used by the --starter
// test) does not, and the point here is to assert on runtime git — not to
// mask a missing binary with a postCreateCommand.
func TestRealDevpodCloneAndDelete(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-devpod --clone E2E in -short mode")
	}

	ctx := integration.TestCtx(t, 10*time.Minute)

	c, _ := integration.StartReadyCircuit(ctx, t, false)

	// Bare repo lives under the bind-mounted shared scratch so file:// URLs
	// resolve identically on the circuit (where lakitu runs) and on the
	// outer dockerd (which bind-mounts workspace sources when building a
	// devpod container). StageCloneFixture writes a repo named
	// "clone-fixture" with a devcontainer.json that lands git-equipped.
	repoURL := c.StageCloneFixture(ctx, "clone-fixture", map[string]string{
		"README.md":                       "# clone fixture\n",
		".devcontainer/devcontainer.json": `{"image":"mcr.microsoft.com/devcontainers/base:debian"}` + "\n",
	})

	kart := c.KartName("clone")
	baseline := integration.DevcontainerIDs(ctx, t)

	stdout, stderr, code := c.Drift(ctx, "new", kart,
		"--tune", "none",
		"--clone", repoURL,
	)
	if code != 0 {
		t.Fatalf("drift new --clone: code=%d\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}

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
	created := integration.SetDiff(afterUp, baseline)
	if len(created) == 0 {
		t.Fatalf("no new devcontainer on host after drift new --clone; baseline=%v after=%v", baseline, afterUp)
	}

	// Locate the workspace container so we can introspect its filesystem
	// directly. devpod labels every container it builds with the
	// dev.containers.id we captured in `created`; pick the first one.
	var wcID string
	for id := range integration.SetDiffSet(afterUp, baseline) {
		wcID = integration.WorkspaceContainerName(ctx, t, id)
		if wcID != "" {
			break
		}
	}
	if wcID == "" {
		t.Fatalf("could not resolve workspace container from created ids: %v", created)
	}

	// git binary must be on PATH: --clone preserves history, so the
	// operator needs git at runtime to use it.
	if out, err := integration.DockerExec(ctx, wcID, "sh", "-c", "command -v git"); err != nil || strings.TrimSpace(string(out)) == "" {
		t.Errorf("git missing in workspace container %s: err=%v out=%q", wcID, err, out)
	}

	// .git/ must be preserved in the cloned workspace — the difference
	// between --clone and --starter is history retention. devpod mounts
	// the clone at /workspaces/<kart-name>/.
	gitDir := "/workspaces/" + kart + "/.git"
	if out, err := integration.DockerExec(ctx, wcID, "sh", "-c", "ls -1A "+gitDir+" | head -1"); err != nil || strings.TrimSpace(string(out)) == "" {
		t.Errorf(".git/ not preserved in workspace %s at %s: err=%v out=%q", wcID, gitDir, err, out)
	}

	// `-y` skips the destructive confirmation prompt (required on
	// non-TTY stdin).
	_, stderr, code = c.Drift(ctx, "kart", "delete", "-y", kart)
	if code != 0 {
		t.Fatalf("drift kart delete: code=%d stderr=%q", code, stderr)
	}
	afterDelete := integration.DevcontainerIDs(ctx, t)
	if orphans := integration.SetDiff(afterDelete, baseline); len(orphans) > 0 {
		t.Errorf("devcontainer orphans after drift kart delete: %v", orphans)
	}
}
