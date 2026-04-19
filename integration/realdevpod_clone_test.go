//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	c := integration.StartCircuit(ctx, t)
	if err := integration.SSHCommand(ctx, c, "lakitu", "init"); err != nil {
		t.Fatalf("lakitu init: %v", err)
	}
	c.RegisterCircuit(ctx, "test")

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
	baseline := devcontainerIDs(ctx, t)

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

	afterUp := devcontainerIDs(ctx, t)
	created := setDiff(afterUp, baseline)
	if len(created) == 0 {
		t.Fatalf("no new devcontainer on host after drift new --clone; baseline=%v after=%v", baseline, afterUp)
	}

	// Locate the workspace container so we can introspect its filesystem
	// directly. devpod labels every container it builds with the
	// dev.containers.id we captured in `created`; pick the first one.
	var wcID string
	for id := range setDiffSet(afterUp, baseline) {
		wcID = workspaceContainerName(ctx, t, id)
		if wcID != "" {
			break
		}
	}
	if wcID == "" {
		t.Fatalf("could not resolve workspace container from created ids: %v", created)
	}

	// git binary must be on PATH: --clone preserves history, so the
	// operator needs git at runtime to use it.
	if out, err := dockerExec(ctx, wcID, "sh", "-c", "command -v git"); err != nil || strings.TrimSpace(string(out)) == "" {
		t.Errorf("git missing in workspace container %s: err=%v out=%q", wcID, err, out)
	}

	// .git/ must be preserved in the cloned workspace — the difference
	// between --clone and --starter is history retention. devpod mounts
	// the clone at /workspaces/<kart-name>/.
	gitDir := "/workspaces/" + kart + "/.git"
	if out, err := dockerExec(ctx, wcID, "sh", "-c", "ls -1A "+gitDir+" | head -1"); err != nil || strings.TrimSpace(string(out)) == "" {
		t.Errorf(".git/ not preserved in workspace %s at %s: err=%v out=%q", wcID, gitDir, err, out)
	}

	// `-y` skips the destructive confirmation prompt (required on
	// non-TTY stdin).
	_, stderr, code = c.Drift(ctx, "delete", "-y", kart)
	if code != 0 {
		t.Fatalf("drift delete: code=%d stderr=%q", code, stderr)
	}
	afterDelete := devcontainerIDs(ctx, t)
	if orphans := setDiff(afterDelete, baseline); len(orphans) > 0 {
		t.Errorf("devcontainer orphans after drift delete: %v", orphans)
	}
}

// setDiffSet returns the members of a not present in b as a set.
func setDiffSet(a, b map[string]struct{}) map[string]struct{} {
	out := map[string]struct{}{}
	for k := range a {
		if _, ok := b[k]; !ok {
			out[k] = struct{}{}
		}
	}
	return out
}

// workspaceContainerName resolves a dev.containers.id label back to the
// container name docker exec can target. Returns "" if no container matches.
func workspaceContainerName(ctx context.Context, t *testing.T, devContainerID string) string {
	t.Helper()
	out, err := exec.CommandContext(ctx, "docker", "ps",
		"--filter", "label=dev.containers.id="+devContainerID,
		"--format", "{{.Names}}").Output()
	if err != nil {
		t.Fatalf("docker ps for label %q: %v", devContainerID, err)
	}
	name := strings.TrimSpace(string(out))
	if name == "" || strings.ContainsRune(name, '\n') {
		// Either no match or multiple — the first-line fallback handles the
		// latter since devpod typically builds one container per workspace.
		fields := strings.Fields(name)
		if len(fields) > 0 {
			return fields[0]
		}
		return ""
	}
	return name
}

// dockerExec runs a command inside a container and returns stdout. Used to
// poke at the workspace filesystem without routing through devpod ssh
// (which would need a full shell session in a -t pty context).
func dockerExec(ctx context.Context, container string, args ...string) ([]byte, error) {
	argv := append([]string{"exec", container}, args...)
	cmd := exec.CommandContext(ctx, "docker", argv...)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("docker exec %s %v: %w (stderr=%q)", container, args, err, ee.Stderr)
		}
		return nil, err
	}
	return out, nil
}
