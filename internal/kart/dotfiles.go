package kart

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type DotfilesResult struct {
	// Path is the tmpdir root; the caller removes it once the kart is up.
	Path          string
	InstallScript string
	// Has* fields let tests assert which sidecar files were emitted.
	HasGitConfig bool
	HasGhHosts   bool
	HasSSHKey    bool
}

// WriteLayer1Dotfiles materializes the character-layer dotfiles tree:
// gitconfig, gh hosts.yml (when PAT present), git-credentials, ssh key +
// config (when SSH key set), plus install.sh that places everything under
// $HOME inside the container. A nil character produces a tmpdir with a
// no-op install.sh so callers can pass it unconditionally.
func WriteLayer1Dotfiles(tmpDir string, char *Character) (*DotfilesResult, error) {
	if tmpDir == "" {
		return nil, fmt.Errorf("dotfiles: tmpDir is required")
	}
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return nil, fmt.Errorf("dotfiles: mkdir %s: %w", tmpDir, err)
	}

	res := &DotfilesResult{Path: tmpDir}

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

	// devpod's `install-dotfiles --repository` (skevetter fork v0.22) git
	// clones the URL, so a file:// URL must point at a real git repo. Stamp
	// an ephemeral one; it's cleaned up with the tmpdir.
	if err := initEphemeralGitRepo(tmpDir); err != nil {
		return nil, err
	}
	return res, nil
}

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
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return fmt.Errorf("dotfiles: write gitconfig: %w", err)
	}
	return nil
}

// writeGhHosts emits a single-host (github.com) hosts.yml — MVP has no way
// to target GHE instances.
func writeGhHosts(tmpDir string, char *Character) error {
	var b strings.Builder
	b.WriteString("github.com:\n")
	b.WriteString("  oauth_token: " + char.PAT + "\n")
	if char.GithubUser != "" {
		b.WriteString("  user: " + char.GithubUser + "\n")
	}
	b.WriteString("  git_protocol: https\n")
	path := filepath.Join(tmpDir, "gh_hosts.yml")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return fmt.Errorf("dotfiles: write gh_hosts.yml: %w", err)
	}
	return nil
}

// writeGitCredentials falls back to a username of `oauth` when no
// github_user is set — GitHub accepts any non-empty name paired with a PAT.
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

func copySSHKey(tmpDir string, char *Character) error {
	data, err := os.ReadFile(char.SSHKeyPath)
	if err != nil {
		return fmt.Errorf("dotfiles: read ssh key %s: %w", char.SSHKeyPath, err)
	}
	keyOut := filepath.Join(tmpDir, "ssh_id")
	if err := os.WriteFile(keyOut, data, 0o600); err != nil { //nolint:gosec // G703: tmpDir is server-controlled, not user input.
		return fmt.Errorf("dotfiles: write ssh_id: %w", err)
	}
	cfg := "Host *\n\tIdentityFile ~/.ssh/id_drift\n\tIdentitiesOnly yes\n"
	cfgOut := filepath.Join(tmpDir, "ssh_config")
	if err := os.WriteFile(cfgOut, []byte(cfg), 0o600); err != nil {
		return fmt.Errorf("dotfiles: write ssh_config: %w", err)
	}
	return nil
}

func writeInstallScript(tmpDir string, res *DotfilesResult, char *Character) (string, error) {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
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
		// Append an Include idempotently — grep+echo stays POSIX.
		b.WriteString("touch \"$HOME/.ssh/config\"\n")
		b.WriteString("chmod 600 \"$HOME/.ssh/config\"\n")
		b.WriteString("grep -q '^Include ~/.ssh/config.drift$' \"$HOME/.ssh/config\" 2>/dev/null || " +
			"printf 'Include ~/.ssh/config.drift\\n' >> \"$HOME/.ssh/config\"\n")
	}

	// devpod's dotfiles runner must see something to execute even when no
	// character is attached.
	if b.Len() == 0 || (!res.HasGitConfig && !res.HasGhHosts && !res.HasSSHKey) {
		b.WriteString(": # no character attached — nothing to install\n")
	}
	_ = char

	path := filepath.Join(tmpDir, "install.sh")
	if err := os.WriteFile(path, []byte(b.String()), 0o700); err != nil { //nolint:gosec // G306: install.sh needs the owner exec bit.
		return "", fmt.Errorf("dotfiles: write install.sh: %w", err)
	}
	return path, nil
}
