package kart

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteLayer1DotfilesEmpty(t *testing.T) {
	dir := t.TempDir()
	res, err := WriteLayer1Dotfiles(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.HasGitConfig || res.HasGhHosts || res.HasSSHKey {
		t.Fatalf("no character but some flags set: %+v", res)
	}
	script, err := os.ReadFile(res.InstallScript)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(script), "#!/bin/sh") {
		t.Fatalf("install script missing shebang: %q", script)
	}
}

func TestWriteLayer1DotfilesFull(t *testing.T) {
	keyFile := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(keyFile, []byte("FAKE-SSH-KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	res, err := WriteLayer1Dotfiles(dir, &Character{
		GitName:    "Kurisu",
		GitEmail:   "k@example.com",
		GithubUser: "kurisu",
		PAT:        "ghp_abcdef",
		SSHKeyPath: keyFile,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.HasGitConfig || !res.HasGhHosts || !res.HasSSHKey {
		t.Fatalf("expected all flags set: %+v", res)
	}

	gc, err := os.ReadFile(filepath.Join(dir, "gitconfig"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gc), "Kurisu") || !strings.Contains(string(gc), "k@example.com") {
		t.Fatalf("gitconfig: %s", gc)
	}

	hosts, err := os.ReadFile(filepath.Join(dir, "gh_hosts.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(hosts), "ghp_abcdef") {
		t.Fatalf("gh_hosts.yml: %s", hosts)
	}

	creds, err := os.ReadFile(filepath.Join(dir, "git_credentials"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(creds), "kurisu:ghp_abcdef@github.com") {
		t.Fatalf("git_credentials: %s", creds)
	}

	sshKey, err := os.ReadFile(filepath.Join(dir, "ssh_id"))
	if err != nil {
		t.Fatal(err)
	}
	if string(sshKey) != "FAKE-SSH-KEY" {
		t.Fatalf("ssh_id: %q", sshKey)
	}

	script, err := os.ReadFile(res.InstallScript)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), "credential.helper store") {
		t.Fatalf("install.sh missing credential helper: %s", script)
	}
	if !strings.Contains(string(script), "Include ~/.ssh/config.drift") {
		t.Fatalf("install.sh missing ssh include: %s", script)
	}
}
