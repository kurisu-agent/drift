//go:build integration

package integration_test

import (
	"strings"
	"testing"
	"time"

	"github.com/kurisu-agent/drift/integration"
	"github.com/kurisu-agent/drift/internal/wire"
)

// TestGhAuthPostUpScript verifies the gh-auth fragment (plan 19) lands
// in the post-up `devpod ssh` invocation's stdin when a kart is created
// with a character that carries a PAT. Walks the full pipeline:
//
//  1. Stash a PAT value in the chest.
//  2. Register a character pointing at the chest entry via pat_secret.
//  3. Create a kart against that character.
//  4. Find the recorded `devpod ssh <kart> --command bash -s` invocation.
//  5. Assert the captured stdin payload contains the gh-auth lines
//     (git config, gh auth login --with-token, gh auth setup-git) AND
//     the resolved PAT — proving server-side dechest reached the
//     fragment without ever appearing on argv.
//
// Replaces the layer-1 `install-dotfiles` test that exercised the
// dead delivery channel removed in plan 19.
func TestGhAuthPostUpScript(t *testing.T) {
	ctx := integration.TestCtx(t, 5*time.Minute)

	c, rec := integration.StartReadyCircuit(ctx, t, true)

	const (
		patSecretName = "alice-pat"
		patValue      = "github_pat_testtoken_abcdef"
	)
	if _, err := c.LakituRPC(ctx, wire.MethodChestNew, map[string]string{
		"name": patSecretName, "value": patValue,
	}); err != nil {
		t.Fatalf("chest.new: %v", err)
	}
	if _, err := c.LakituRPC(ctx, wire.MethodCharacterNew, map[string]string{
		"name":        "alice",
		"git_name":    "Alice Example",
		"git_email":   "alice@example.com",
		"github_user": "alice",
		"pat_secret":  "chest:" + patSecretName,
	}); err != nil {
		t.Fatalf("character.new: %v", err)
	}

	kart := c.KartName("gh-auth")
	if _, stderr, code := c.Drift(ctx, "new", kart,
		"--tune", "none",
		"--character", "alice",
	); code != 0 {
		t.Fatalf("drift new: code=%d stderr=%q", code, stderr)
	}

	inv := rec.FindSSH(ctx)
	if inv == nil {
		t.Fatalf("no devpod ssh invocation recorded")
	}
	// Argv carries the literal `bash -s` so callers piping a script over
	// stdin replace the historical `--command <script>` argv exposure.
	if !integration.ArgvHas(inv.Argv, "--command", "bash -s") {
		t.Errorf("ssh argv = %v, want --command 'bash -s'", inv.Argv)
	}

	// The stdin artifact must contain the full post-up script with the
	// gh-auth fragment spliced in. Any of these lines missing means the
	// fragment regressed (or was never wired).
	stdin := string(c.ReadArtifact(ctx, inv, "stdin"))
	for _, want := range []string{
		`git config --global user.name 'Alice Example'`,
		`git config --global user.email 'alice@example.com'`,
		`git config --global github.user 'alice'`,
		`gh auth login --with-token --hostname github.com`,
		`gh auth setup-git --hostname github.com`,
		// Server dechested the PAT before kart.new saw the Character;
		// the literal token must appear in the stdin payload (which is
		// what proves stdin — not argv — is the delivery channel).
		patValue,
	} {
		if !strings.Contains(stdin, want) {
			t.Errorf("post-up stdin missing %q\nfull stdin:\n%s", want, stdin)
		}
	}

	// Argv must NOT carry the PAT — the whole point of the stdin pipe
	// is keeping the secret off the argv table.
	for _, a := range inv.Argv {
		if strings.Contains(a, patValue) {
			t.Errorf("PAT leaked into ssh argv: %v", inv.Argv)
			break
		}
	}
}
