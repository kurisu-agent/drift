//go:build integration

package integration_test

import (
	"strings"
	"testing"
	"time"

	"github.com/kurisu-agent/drift/integration"
	"github.com/kurisu-agent/drift/internal/wire"
)

// TestRealDevpodDenyLiteralsHook is the end-to-end smoke for plan 20.
// Builds a real workspace via devpod against a circuit configured with
// `deny_literals: chest:<name>` and asserts the three files land inside
// the kart's $HOME:
//
//   - $HOME/.claude/hooks/block-literals.sh — the always-installed hook
//     script (verbatim from internal/seed/builtins.go's
//     blockLiteralsHookScript constant).
//   - $HOME/.claude/deny-literals.txt — the dechested literal content,
//     byte-for-byte from the chest entry.
//   - $HOME/.claude/settings.json — drift's opinionated settings file
//     with the PreToolUse hook block always wired.
//
// Skipped under -short because real devpod up + container lifecycle
// adds ~2 min wallclock.
func TestRealDevpodDenyLiteralsHook(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-devpod deny-literals E2E in -short mode")
	}

	ctx := integration.TestCtx(t, 6*time.Minute)

	c, _ := integration.StartReadyCircuit(ctx, t, false)

	const denyListContent = "alpha\n# comment\nbeta gamma\nDelta\n"

	if _, err := c.LakituRPC(ctx, wire.MethodChestNew, map[string]string{
		"name":  "deny-list",
		"value": denyListContent,
	}); err != nil {
		t.Fatalf("chest.new: %v", err)
	}
	if _, err := c.LakituRPC(ctx, wire.MethodConfigSet, map[string]string{
		"key":   "deny_literals",
		"value": "chest:deny-list",
	}); err != nil {
		t.Fatalf("config.set deny_literals: %v", err)
	}

	starterURL := c.StageStarter(ctx, "deny-starter", map[string]string{
		"README.md":                       "# deny starter\n",
		".devcontainer/devcontainer.json": `{"image":"debian:bookworm-slim"}` + "\n",
	})

	if _, err := c.LakituRPC(ctx, wire.MethodTuneNew, map[string]any{
		"name":    "denytune",
		"starter": starterURL,
		"seed":    []string{"claudeCode"},
	}); err != nil {
		t.Fatalf("tune.new: %v", err)
	}

	kart := c.KartName("deny")
	baseline := integration.DevcontainerIDs(ctx, t)

	stdout, stderr, code := c.Drift(ctx, "new", kart, "--tune", "denytune")
	if code != 0 {
		t.Fatalf("drift new: code=%d\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}
	t.Cleanup(func() {
		_, _, _ = c.Drift(ctx, "kart", "delete", "-y", kart)
	})

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

	// Hook script: must contain the PreToolUse plumbing and the deny-list
	// scan loop. We don't pin the full body so future edits to the
	// constant don't break this test, but the load-bearing markers must
	// survive any rewrite.
	hookRaw, err := integration.DockerExec(ctx, wcID, "sh", "-c", `cat "$HOME/.claude/hooks/block-literals.sh"`)
	if err != nil {
		t.Fatalf("read block-literals.sh: %v", err)
	}
	hook := string(hookRaw)
	for _, want := range []string{
		"PreToolUse",
		"deny-literals.txt",
		"permissionDecision",
		"grep -F -i",
		"jq -r",
	} {
		if !strings.Contains(hook, want) {
			t.Errorf("block-literals.sh missing %q\n--- script ---\n%s", want, hook)
		}
	}

	// Deny-list: byte-for-byte the chest entry.
	denyRaw, err := integration.DockerExec(ctx, wcID, "sh", "-c", `cat "$HOME/.claude/deny-literals.txt"`)
	if err != nil {
		t.Fatalf("read deny-literals.txt: %v", err)
	}
	if string(denyRaw) != denyListContent {
		t.Errorf("deny-literals.txt =\n%q\nwant\n%q", string(denyRaw), denyListContent)
	}

	// settings.json: hooks block must always be present, regardless of
	// whether the deny-list is configured.
	settingsRaw, err := integration.DockerExec(ctx, wcID, "sh", "-c", `cat "$HOME/.claude/settings.json"`)
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}
	settings := string(settingsRaw)
	for _, want := range []string{
		`"hooks":`,
		`"PreToolUse"`,
		`"matcher": "Bash|Edit|Write|MultiEdit"`,
		`bash $HOME/.claude/hooks/block-literals.sh`,
	} {
		if !strings.Contains(settings, want) {
			t.Errorf("settings.json missing %q:\n%s", want, settings)
		}
	}

	if _, _, code := c.Drift(ctx, "kart", "delete", "-y", kart); code != 0 {
		t.Errorf("drift kart delete: code=%d", code)
	}
}
