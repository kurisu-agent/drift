package kart

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// DotfilesResult describes what WriteLayer1Dotfiles produced. Path is the
// tmpdir holding the generated files; the caller passes it to devpod as
// `--dotfiles file://<path>` (or an install-dotfiles invocation, TBD) and
// removes it when the kart is up.
type DotfilesResult struct {
	// Path is the root of the generated layer-1 dotfiles tree.
	Path string
	// InstallScript is the full path to the install.sh inside Path that
	// copies the generated files into place when devpod runs dotfiles.
	InstallScript string
	// HasGitConfig / HasGhHosts / HasSSHKey let tests assert which optional
	// sidecar files the generator emitted for a given character.
	HasGitConfig bool
	HasGhHosts   bool
	HasSSHKey    bool
}

// WriteLayer1Dotfiles materializes the layer-1 dotfiles tree described in
// plans/PLAN.md § Dotfiles injection (character layer). It creates:
//
//   - gitconfig            — ~/.gitconfig lines from the character
//   - gh_hosts.yml         — ~/.config/gh/hosts.yml when a PAT is present
//   - git_credentials      — ~/.git-credentials seeding the PAT for HTTPS
//   - ssh/id_<name>        — copy of the character's SSH key when set
//   - ssh/config           — per-host entry pointing ssh at the copied key
//   - install.sh           — POSIX shell that places the above files and
//                            wires the git credential helper
//
// When character is nil (no character attached and no default), the
// function still produces a tmpDir with an install.sh that is a no-op — the
// caller can unconditionally pass it to devpod.
//
// Returning the script path (rather than just tmpDir) saves the caller from
// having to rebuild it every time we change the internal layout.
func WriteLayer1Dotfiles(tmpDir string, char *Character) (*DotfilesResult, error) {
	if tmpDir == "" {
		return nil, fmt.Errorf("dotfiles: tmpDir is required")
	}
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return nil, fmt.Errorf("dotfiles: mkdir %s: %w", tmpDir, err)
	}

	res := &DotfilesResult{Path: tmpDir}

	// Write the sidecar files first, then generate install.sh that knows
	// what to copy. Order matters only because install.sh references the
	// filenames we've agreed on here.
	if char != nil && (char.GitName != "" || char.GitEmail != "" || char.GithubUser != "") {
		if err := writeGitConfig(tmpDir, char); err != nil {
			return nil, err
		}
		res.HasGitConfig = true
	}
	if char != nil && char.PAT != "" {
		if err := writeGhHosts(tmpDir, char); err != nil {
			return nil, err
		}
		if err := writeGitCredentials(tmpDir, char); err != nil {
			return nil, err
		}
		res.HasGhHosts = true
	}
	if char != nil && char.SSHKeyPath != "" {
		if err := copySSHKey(tmpDir, char); err != nil {
			return nil, err
		}
		res.HasSSHKey = true
	}

	scriptPath, err := writeInstallScript(tmpDir, res, char)
	if err != nil {
		return nil, err
	}
	res.InstallScript = scriptPath

	// devpod's `install-dotfiles --repository` (skevetter fork v0.17) runs
	// a git clone against the URL we pass. file:// URLs are valid targets
	// only when the directory is a git repo, so stamp an ephemeral repo in
	// place. The commit never leaves this tmpdir — we clean it up once the
	// kart is up — so author values are cosmetic.
	if err := initEphemeralGitRepo(tmpDir); err != nil {
		return nil, err
	}
	return res, nil
}

// initEphemeralGitRepo runs `git init && git add -A && git commit` inside
// dir so devpod's dotfiles cloner accepts the directory as a valid source.
// All three invocations are best-effort: a failure leaves the tmpdir
// usable for non-git tooling, so we surface the error only if the initial
// `git init` fails outright.
func initEphemeralGitRepo(dir string) error {
	gitEnv := append(os.Environ(),
		"GIT_AUTHOR_NAME=drift", "GIT_AUTHOR_EMAIL=noreply@drift.local",
		"GIT_COMMITTER_NAME=drift", "GIT_COMMITTER_EMAIL=noreply@drift.local",
	)
	for _, args := range [][]string{
		{"init", "--quiet", "-b", "main"},
		{"add", "-A"},
		{"commit", "--quiet", "-m", "drift layer-1 dotfiles"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = gitEnv
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("dotfiles: git %s: %w: %s", strings.Join(args, " "), err, string(out))
		}
	}
	return nil
}

// writeGitConfig produces a minimal gitconfig — only the fields PLAN.md
// specifies for layer 1 (user.name, user.email, github.user).
func writeGitConfig(tmpDir string, char *Character) error {
	var b strings.Builder
	if char.GitName != "" || char.GitEmail != "" {
		b.WriteString("[user]\n")
		if char.GitName != "" {
			fmt.Fprintf(&b, "\tname = %s\n", char.GitName)
		}
		if char.GitEmail != "" {
			fmt.Fprintf(&b, "\temail = %s\n", char.GitEmail)
		}
	}
	if char.GithubUser != "" {
		fmt.Fprintf(&b, "[github]\n\tuser = %s\n", char.GithubUser)
	}
	path := filepath.Join(tmpDir, "gitconfig")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("dotfiles: write gitconfig: %w", err)
	}
	return nil
}

// writeGhHosts emits a minimal ~/.config/gh/hosts.yml so `gh` picks up the
// PAT automatically. Single-host (github.com) because we have no way in
// MVP to target GHE instances.
func writeGhHosts(tmpDir string, char *Character) error {
	var b strings.Builder
	b.WriteString("github.com:\n")
	b.WriteString("  oauth_token: " + char.PAT + "\n")
	if char.GithubUser != "" {
		b.WriteString("  user: " + char.GithubUser + "\n")
	}
	b.WriteString("  git_protocol: https\n")
	path := filepath.Join(tmpDir, "gh_hosts.yml")
	// 0600 — this file carries a live access token.
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return fmt.Errorf("dotfiles: write gh_hosts.yml: %w", err)
	}
	return nil
}

// writeGitCredentials creates the ~/.git-credentials line the `store`
// helper consumes — `https://<user>:<pat>@github.com`. We fall back to a
// username of `oauth` when no github_user is set; GitHub accepts any
// non-empty name paired with a PAT.
func writeGitCredentials(tmpDir string, char *Character) error {
	user := char.GithubUser
	if user == "" {
		user = "oauth"
	}
	line := fmt.Sprintf("https://%s:%s@github.com\n", user, char.PAT)
	path := filepath.Join(tmpDir, "git_credentials")
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		return fmt.Errorf("dotfiles: write git_credentials: %w", err)
	}
	return nil
}

// copySSHKey copies the character's SSH private key to <tmpDir>/ssh_id and
// writes an ssh/config sibling that points ssh at the key for all hosts.
// The key file is 0600; the ssh config is 0644.
func copySSHKey(tmpDir string, char *Character) error {
	data, err := os.ReadFile(char.SSHKeyPath)
	if err != nil {
		return fmt.Errorf("dotfiles: read ssh key %s: %w", char.SSHKeyPath, err)
	}
	keyOut := filepath.Join(tmpDir, "ssh_id")
	if err := os.WriteFile(keyOut, data, 0o600); err != nil {
		return fmt.Errorf("dotfiles: write ssh_id: %w", err)
	}
	cfg := "Host *\n\tIdentityFile ~/.ssh/id_drift\n\tIdentitiesOnly yes\n"
	cfgOut := filepath.Join(tmpDir, "ssh_config")
	if err := os.WriteFile(cfgOut, []byte(cfg), 0o644); err != nil {
		return fmt.Errorf("dotfiles: write ssh_config: %w", err)
	}
	return nil
}

// writeInstallScript generates install.sh — a POSIX-sh script that copies
// each generated file to its target location under $HOME inside the
// container. devpod invokes this when --dotfiles points at the tmp dir.
func writeInstallScript(tmpDir string, res *DotfilesResult, char *Character) (string, error) {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("# Generated by drift; layer-1 dotfiles from the active character.\n")
	b.WriteString("set -eu\n")
	b.WriteString(`cd "$(dirname "$0")"` + "\n")

	if res.HasGitConfig {
		b.WriteString("cp gitconfig \"$HOME/.gitconfig\"\n")
		b.WriteString("chmod 644 \"$HOME/.gitconfig\"\n")
	}
	if res.HasGhHosts {
		b.WriteString("mkdir -p \"$HOME/.config/gh\"\n")
		b.WriteString("cp gh_hosts.yml \"$HOME/.config/gh/hosts.yml\"\n")
		b.WriteString("chmod 600 \"$HOME/.config/gh/hosts.yml\"\n")
		b.WriteString("cp git_credentials \"$HOME/.git-credentials\"\n")
		b.WriteString("chmod 600 \"$HOME/.git-credentials\"\n")
		b.WriteString("git config --global credential.helper store\n")
	}
	if res.HasSSHKey {
		b.WriteString("mkdir -p \"$HOME/.ssh\"\n")
		b.WriteString("chmod 700 \"$HOME/.ssh\"\n")
		b.WriteString("cp ssh_id \"$HOME/.ssh/id_drift\"\n")
		b.WriteString("chmod 600 \"$HOME/.ssh/id_drift\"\n")
		b.WriteString("cp ssh_config \"$HOME/.ssh/config.drift\"\n")
		b.WriteString("chmod 644 \"$HOME/.ssh/config.drift\"\n")
		// Append an Include line idempotently — grep+echo stays POSIX.
		b.WriteString("touch \"$HOME/.ssh/config\"\n")
		b.WriteString("chmod 600 \"$HOME/.ssh/config\"\n")
		b.WriteString("grep -q '^Include ~/.ssh/config.drift$' \"$HOME/.ssh/config\" 2>/dev/null || " +
			"printf 'Include ~/.ssh/config.drift\\n' >> \"$HOME/.ssh/config\"\n")
	}

	// Ensure the script is never empty — devpod's dotfiles runner must see
	// something to execute even when no character is attached.
	if b.Len() == 0 || (!res.HasGitConfig && !res.HasGhHosts && !res.HasSSHKey) {
		b.WriteString(": # no character attached — nothing to install\n")
	}
	// Silence unused-character: the byte is consumed above but go vet wants
	// the param referenced somewhere in the function.
	_ = char

	path := filepath.Join(tmpDir, "install.sh")
	if err := os.WriteFile(path, []byte(b.String()), 0o755); err != nil {
		return "", fmt.Errorf("dotfiles: write install.sh: %w", err)
	}
	return path, nil
}
