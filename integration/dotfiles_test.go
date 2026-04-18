//go:build integration

package integration_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kurisu-agent/drift/internal/wire"
)

// TestLayer1Dotfilesland verifies that when a character is attached to a
// kart, drift generates the expected layer-1 dotfiles tree and hands it to
// devpod's install-dotfiles helper. The shim preserves the tmpdir that
// would otherwise be cleaned up on return — we assert on install.sh,
// gitconfig, gh_hosts.yml, and git_credentials contents end-to-end.
//
// PAT is stored in the chest and referenced as `chest:<name>` on the
// character; the server resolves it before kart.new sees the Character, so
// the generated git_credentials line carries the literal token. That is
// exactly the plans/PLAN.md § Dotfiles injection contract and the behavior
// the upstream unit tests exercise in isolation — this test validates it
// survives the full RPC → lakitu → kart.new → devpod pipeline.
func TestLayer1Dotfilesland(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	c, rec := setupTuneCircuit(ctx, t)

	const (
		patSecretName = "alice-pat"
		patValue      = "ghp_testtoken_abcdef"
	)
	if _, err := c.LakituRPC(ctx, wire.MethodChestSet, map[string]string{
		"name": patSecretName, "value": patValue,
	}); err != nil {
		t.Fatalf("chest.set: %v", err)
	}
	if _, err := c.LakituRPC(ctx, wire.MethodCharacterAdd, map[string]string{
		"name":        "alice",
		"git_name":    "Alice Example",
		"git_email":   "alice@example.com",
		"github_user": "alice",
		"pat_secret":  "chest:" + patSecretName,
	}); err != nil {
		t.Fatalf("character.add: %v", err)
	}

	kart := c.KartName("df-layer1")
	if _, stderr, code := c.Drift(ctx, "new", kart,
		"--tune", "none",
		"--character", "alice",
	); code != 0 {
		t.Fatalf("drift new: code=%d stderr=%q", code, stderr)
	}

	// install-dotfiles is where the layer-1 tmpdir lands. Find that
	// invocation; the shim has preserved its --dotfiles file://<path> tree
	// under <artifactDir>/dotfiles/.
	inv := rec.FindInstallDotfiles(ctx)
	if inv == nil {
		t.Fatalf("no install-dotfiles invocation recorded")
	}
	files := c.ListArtifact(ctx, inv, "dotfiles")
	want := map[string]bool{
		"install.sh":      false,
		"gitconfig":       false,
		"gh_hosts.yml":    false,
		"git_credentials": false,
	}
	for _, f := range files {
		if _, ok := want[f]; ok {
			want[f] = true
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("dotfiles/%s missing (got %v)", name, files)
		}
	}

	// gitconfig must carry the character's identity.
	gc := string(c.ReadArtifact(ctx, inv, "dotfiles/gitconfig"))
	for _, want := range []string{"name = Alice Example", "email = alice@example.com", "user = alice"} {
		if !strings.Contains(gc, want) {
			t.Errorf("gitconfig missing %q:\n%s", want, gc)
		}
	}

	// git_credentials must include the *resolved* PAT — server looked it
	// up via chest before handing it to the dotfiles generator.
	creds := strings.TrimSpace(string(c.ReadArtifact(ctx, inv, "dotfiles/git_credentials")))
	wantCreds := "https://alice:" + patValue + "@github.com"
	if creds != wantCreds {
		t.Errorf("git_credentials = %q, want %q", creds, wantCreds)
	}

	// gh_hosts.yml carries the PAT as oauth_token.
	gh := string(c.ReadArtifact(ctx, inv, "dotfiles/gh_hosts.yml"))
	if !strings.Contains(gh, "oauth_token: "+patValue) {
		t.Errorf("gh_hosts.yml missing resolved PAT:\n%s", gh)
	}
	if !strings.Contains(gh, "user: alice") {
		t.Errorf("gh_hosts.yml missing github_user:\n%s", gh)
	}

	// install.sh should be a POSIX shell script and reference the files
	// we just verified. Catching a regression where we generate a script
	// that doesn't actually copy things into $HOME.
	install := string(c.ReadArtifact(ctx, inv, "dotfiles/install.sh"))
	if !strings.HasPrefix(install, "#!/bin/sh") {
		t.Errorf("install.sh shebang = %q, want #!/bin/sh", strings.SplitN(install, "\n", 2)[0])
	}
	for _, want := range []string{
		`cp gitconfig "$HOME/.gitconfig"`,
		`cp gh_hosts.yml "$HOME/.config/gh/hosts.yml"`,
		`cp git_credentials "$HOME/.git-credentials"`,
	} {
		if !strings.Contains(install, want) {
			t.Errorf("install.sh missing %q:\n%s", want, install)
		}
	}

	// Final sanity: the --repository flag on install-dotfiles is the
	// file:// URL of the same tmpdir (skevetter fork v0.22 — the flag name
	// differs from upstream devpod's `--dotfiles`). Argv-level check so a
	// regression that breaks the URL scheme fails loudly.
	u := argvValue(inv.Argv, "--repository")
	if !strings.HasPrefix(u, "file:///") {
		t.Errorf("--repository url = %q, want file:///… URL", u)
	}
}
