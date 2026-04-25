package kart

import (
	"strings"
	"testing"
)

func TestSSHLoginAliasFragment_empty(t *testing.T) {
	t.Parallel()
	if got := sshLoginAliasFragment(""); got != "" {
		t.Errorf("empty alias should produce empty fragment, got %q", got)
	}
}

func TestSSHLoginAliasFragment_structure(t *testing.T) {
	t.Parallel()
	got := sshLoginAliasFragment(DriftSSHAlias)
	if got == "" {
		t.Fatal("expected non-empty fragment")
	}
	// Idempotency guard — the whole block gates on getent passwd.
	if !strings.Contains(got, `if ! getent passwd "drifter" >/dev/null; then`) {
		t.Errorf("fragment missing getent guard: %q", got)
	}
	// Privilege detection: prefer root, fall back to sudo, skip
	// gracefully if neither is available. Minimal images
	// (debian:bookworm-slim) ship without sudo and would otherwise
	// fail the whole containerScript.
	for _, want := range []string{
		`if [ "$(id -u)" -eq 0 ]; then`,
		`elif command -v sudo >/dev/null 2>&1; then`,
		`sudo_cmd="__skip__"`,
		`if ! command -v useradd >/dev/null 2>&1; then`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("fragment missing privilege/tool guard %q in:\n%s", want, got)
		}
	}
	// UID discovery comes from /workspaces, not hardcoded.
	if !strings.Contains(got, `stat -c %u /workspaces`) {
		t.Errorf("fragment should pull UID from /workspaces: %q", got)
	}
	// useradd with -o so the duplicate UID is accepted and -M so no
	// new home directory is created. Prefixed by $sudo_cmd so the
	// same call works as root or under sudo.
	if !strings.Contains(got, `$sudo_cmd useradd -o -u`) {
		t.Errorf("fragment should use $sudo_cmd useradd -o -u for same-UID alias: %q", got)
	}
	if !strings.Contains(got, ` -M `) {
		t.Errorf("fragment should pass -M to skip home dir creation: %q", got)
	}
	// Password locked so only key auth works.
	if !strings.Contains(got, `passwd -l "drifter"`) {
		t.Errorf("fragment should lock the password: %q", got)
	}
}
