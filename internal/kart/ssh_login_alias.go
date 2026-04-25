package kart

import (
	"fmt"
	"strings"
)

// DriftSSHAlias is the stable login name workstation ssh uses when
// talking to any kart (`ssh drift.<circuit>.<kart>` → `User drifter`).
// It's a same-UID alias of the kart's primary container user, added
// by sshLoginAliasFragment during kart.new. Keeping the name constant
// across karts is the whole point — otherwise every `ssh`/`scp`/IDE
// caller has to know the upstream image's user (`node`, `vscode`,
// `ubuntu`, …), which varies and forces per-kart ssh_config blocks.
const DriftSSHAlias = "drifter"

// sshLoginAliasFragment returns the bash snippet that installs the
// drift SSH login alias inside the kart. The fragment:
//
//  1. Finds the primary non-root user (the owner of /workspaces).
//     That's whatever upstream user devpod dropped us into — `node`
//     in typescript-node, `vscode` in universal, `ubuntu` in base, or
//     plan-12's per-character username.
//  2. Appends a same-UID `/etc/passwd` entry for `alias` pointing at
//     the primary user's home and shell. Same UID means the kernel
//     sees one identity — every file the primary user owns is
//     accessible to drifter, and vice versa. The alias name is just a
//     second login handle for that identity.
//  3. Adds a locked `/etc/shadow` entry so non-key auth can't bypass
//     the SSH key requirement.
//
// The fragment is idempotent: re-running it on a kart that already
// has the alias is a no-op (skips the useradd if getent finds the
// user). Authorized keys are NOT touched — devpod has already
// seeded the primary user's authorized_keys with the workstation's
// injected key, and since drifter shares that $HOME the same file
// authorizes both logins.
//
// Why no sshd / authorized_keys plumbing: devpod's ssh-server does
// not use OpenSSH sshd. It's a custom Go implementation that `su`s
// to the SSH-login-user before handling the session. So all we need
// is for the name to exist in /etc/passwd and for the target home's
// authorized_keys to contain our key. The second half is already
// true. This fragment adds the first half.
func sshLoginAliasFragment(alias string) string {
	if alias == "" {
		return ""
	}
	var b strings.Builder
	// Skip if the alias already exists — the fragment must be safe to
	// re-run (kart.recreate will re-hit it; retrofitting an existing
	// kart via an ad-hoc `devpod ssh --command` also should be idempotent).
	fmt.Fprintf(&b, `if ! getent passwd %q >/dev/null; then`+"\n", alias)
	// useradd / passwd need root. Most devcontainer images give the
	// primary user passwordless sudo, but minimal bases (debian:slim,
	// alpine without -extras) ship without sudo. Detect both: prefer
	// already-root, fall back to sudo, and skip the whole block with a
	// warning if neither is available — drifter is a usability win,
	// not a hard requirement, so a kart on a minimal image still
	// builds (just without the wildcard ssh alias).
	b.WriteString(`  if [ "$(id -u)" -eq 0 ]; then` + "\n")
	b.WriteString(`    sudo_cmd=""` + "\n")
	b.WriteString(`  elif command -v sudo >/dev/null 2>&1; then` + "\n")
	b.WriteString(`    sudo_cmd="sudo"` + "\n")
	b.WriteString(`  else` + "\n")
	b.WriteString(`    sudo_cmd="__skip__"` + "\n")
	b.WriteString(`  fi` + "\n")
	b.WriteString(`  if [ "$sudo_cmd" = "__skip__" ]; then` + "\n")
	fmt.Fprintf(&b, `    echo "ssh-alias: skipping %s (kart has no root and no sudo)" >&2`+"\n", alias)
	b.WriteString(`  else` + "\n")
	// Resolve the primary user from /workspaces (the canonical devpod
	// mount root). `stat -c %u` gives UID; getent translates UID back
	// to the user's passwd entry so we can copy home/shell verbatim.
	b.WriteString(`    primary_uid=$(stat -c %u /workspaces)` + "\n")
	b.WriteString(`    primary_entry=$(getent passwd "$primary_uid")` + "\n")
	b.WriteString(`    if [ -z "$primary_entry" ]; then echo "ssh-alias: no /etc/passwd entry for uid $primary_uid" >&2; exit 1; fi` + "\n")
	// /etc/passwd is colon-separated name:passwd:uid:gid:gecos:home:shell.
	b.WriteString(`    primary_gid=$(echo "$primary_entry" | cut -d: -f4)` + "\n")
	b.WriteString(`    primary_home=$(echo "$primary_entry" | cut -d: -f6)` + "\n")
	b.WriteString(`    primary_shell=$(echo "$primary_entry" | cut -d: -f7)` + "\n")
	// Append the alias. `-o` allows the duplicate UID; `-M` skips
	// creating a home dir (we're sharing the primary user's home).
	// `useradd` may also be missing on minimal images; treat that the
	// same as no-sudo and degrade gracefully rather than failing the
	// whole kart.new pipeline.
	b.WriteString(`    if ! command -v useradd >/dev/null 2>&1; then` + "\n")
	fmt.Fprintf(&b, `      echo "ssh-alias: skipping %s (kart has no useradd)" >&2`+"\n", alias)
	b.WriteString(`    else` + "\n")
	fmt.Fprintf(&b,
		`      $sudo_cmd useradd -o -u "$primary_uid" -g "$primary_gid" -d "$primary_home" -s "$primary_shell" -M -N %q`+"\n",
		alias)
	// Lock the password so only key auth works. `passwd -l` puts `!`
	// in front of the hash field in /etc/shadow.
	fmt.Fprintf(&b, `      $sudo_cmd passwd -l %q >/dev/null || true`+"\n", alias)
	b.WriteString(`    fi` + "\n")
	b.WriteString(`  fi` + "\n")
	b.WriteString(`fi` + "\n")
	return b.String()
}
