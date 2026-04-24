#!/usr/bin/env bash
# normalize-user.sh — rename the image's default non-root user to the
# kart's character, or create one if the image has no uid-1000 user.
#
# Run as root from an onCreateCommand (devcontainer lifecycle), once per
# container. Idempotent: a re-run against an already-normalised container
# is a no-op.
#
# Usage: normalize-user.sh <character-name>
#
# Exit codes:
#   0  normalised (rename, fresh create, or no-op)
#   2  image lacks tooling to normalise (no usermod/useradd and no way
#      to install shadow). Caller should surface a clear message.
#   3  misuse (bad args).

set -euo pipefail

NEW_USER="${1:-}"
TARGET_UID="${LAKITU_EXPECTED_UID:-1000}"

if [ -z "$NEW_USER" ]; then
  echo "normalize-user: character name is required" >&2
  exit 3
fi
if ! printf '%s' "$NEW_USER" | grep -Eq '^[a-z][a-z0-9-]{0,31}$'; then
  echo "normalize-user: invalid character name '$NEW_USER' (must match [a-z][a-z0-9-]{0,31})" >&2
  exit 3
fi

log() { printf '[normalize-user] %s\n' "$*"; }

if [ "$(id -u)" -ne 0 ]; then
  echo "normalize-user: must be invoked as root (wrapper is expected to elevate via sudo); got uid=$(id -u) user=$(id -un 2>/dev/null || echo '?')" >&2
  exit 4
fi

# Ensure useradd exists for the fresh-create path. The rename path uses
# direct /etc/passwd edits and needs nothing beyond sed, which is in
# every base image we target.
ensure_useradd() {
  if command -v useradd >/dev/null 2>&1; then
    return 0
  fi
  if command -v apk >/dev/null 2>&1; then
    log "installing shadow (alpine) for useradd"
    apk add --no-cache shadow >/dev/null
    return 0
  fi
  if command -v apt-get >/dev/null 2>&1; then
    log "installing passwd (debian fallback) for useradd"
    DEBIAN_FRONTEND=noninteractive apt-get update -qq >/dev/null
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends passwd >/dev/null
    return 0
  fi
  echo "normalize-user: image has no useradd and no apk/apt-get to install it" >&2
  return 2
}

ensure_sudo_file() {
  local user="$1"
  local sudoers_dir="/etc/sudoers.d"
  [ -d "$sudoers_dir" ] || return 0
  local path="$sudoers_dir/$user"
  [ -f "$path" ] && return 0
  printf '%s ALL=(ALL) NOPASSWD:ALL\n' "$user" >"$path"
  chmod 0440 "$path"
}

# Look up the existing non-root user by TARGET_UID. Returns empty if none.
old_user_by_uid() {
  getent passwd "$TARGET_UID" 2>/dev/null | cut -d: -f1
}

OLD_USER="$(old_user_by_uid || true)"

# Case 1: character user already exists at the target uid — idempotent no-op.
if [ "$OLD_USER" = "$NEW_USER" ]; then
  log "user '$NEW_USER' already present at uid $TARGET_UID, nothing to do"
  ensure_sudo_file "$NEW_USER"
  exit 0
fi

# Case 2: no user at TARGET_UID — create one fresh.
if [ -z "$OLD_USER" ]; then
  if ! ensure_useradd; then
    exit 2
  fi
  log "no user at uid $TARGET_UID; creating '$NEW_USER' fresh"
  # Some images ship a group at TARGET_UID without a matching user (rare).
  # useradd -U would collide; fall back to -g <gid> in that case.
  if getent group "$TARGET_UID" >/dev/null 2>&1; then
    useradd -m -s /bin/bash -u "$TARGET_UID" -g "$TARGET_UID" "$NEW_USER"
  else
    useradd -m -s /bin/bash -u "$TARGET_UID" -U "$NEW_USER"
  fi
  # Add to wheel/sudo if either exists — different distros use different names.
  for g in sudo wheel; do
    if getent group "$g" >/dev/null 2>&1; then
      if command -v usermod >/dev/null 2>&1; then
        usermod -aG "$g" "$NEW_USER"
      elif command -v addgroup >/dev/null 2>&1; then
        addgroup "$NEW_USER" "$g" 2>/dev/null || true
      fi
    fi
  done
  ensure_sudo_file "$NEW_USER"
  exit 0
fi

# Case 3: different user holds TARGET_UID — rename it to NEW_USER.
log "renaming '$OLD_USER' (uid $TARGET_UID) → '$NEW_USER'"

OLD_HOME="$(getent passwd "$OLD_USER" | cut -d: -f6)"
NEW_HOME="/home/$NEW_USER"

# Rename via direct edits to /etc/passwd, /etc/shadow, /etc/group,
# /etc/gshadow. usermod refuses to operate while the old user has
# running processes (devpod's agent typically runs as uid 1000 during
# onCreateCommand), and linux accesses are keyed by uid anyway — so
# renaming the text label behind the scenes is safe and sidesteps the
# locking entirely.
sed -i -E "s|^$OLD_USER(:[^:]*:[^:]*:[^:]*:[^:]*:)$OLD_HOME(:.*)$|$NEW_USER\\1$NEW_HOME\\2|" /etc/passwd
sed -i -E "s|^$OLD_USER:|$NEW_USER:|" /etc/passwd
for f in /etc/shadow /etc/gshadow; do
  [ -f "$f" ] && sed -i -E "s|^$OLD_USER:|$NEW_USER:|" "$f"
done
# /etc/group: rename the primary group line (same name as user by
# convention) and rewrite any membership references. Uses `@` as the
# sed delimiter so `|` remains available as an alternation metachar
# inside the group — /etc/group's member lists need `(,|$)` matching
# to catch both mid-list and end-of-line occurrences.
sed -i -E "s|^$OLD_USER:|$NEW_USER:|" /etc/group
sed -i -E "s@:$OLD_USER(,|\$)@:$NEW_USER\\1@g" /etc/group
sed -i -E "s@,$OLD_USER(,|\$)@,$NEW_USER\\1@g" /etc/group

# Move the home directory. Three cases:
#   - old home == new home: nothing to move.
#   - new home doesn't exist: mv old→new.
#   - new home exists (docker pre-created it because a bind mount
#     targets /home/$NEW_USER/<something>): merge old home contents
#     into new home, skipping names that already exist (those are the
#     bind mounts we must not clobber).
if [ "$OLD_HOME" = "$NEW_HOME" ]; then
  : # no-op
elif [ ! -e "$NEW_HOME" ]; then
  mv "$OLD_HOME" "$NEW_HOME"
else
  log "merging $OLD_HOME into pre-existing $NEW_HOME (bind-mount parents)"
  if [ -d "$OLD_HOME" ]; then
    find "$OLD_HOME" -mindepth 1 -maxdepth 1 -print0 | while IFS= read -r -d '' src; do
      base=$(basename "$src")
      [ -e "$NEW_HOME/$base" ] && continue
      mv "$src" "$NEW_HOME/$base"
    done
    rmdir "$OLD_HOME" 2>/dev/null || rm -rf "$OLD_HOME"
  fi
fi
# Ownership is by uid, so the new home's top dir may still carry the
# old name resolution; chown nudges it back to be obvious under `ls -la`.
chown "$TARGET_UID:$TARGET_UID" "$NEW_HOME" 2>/dev/null || true

# Rewrite hardcoded references to the old home path in dotfiles and
# common /etc drop-ins. Bounded scope: no recursive sweep of the whole
# filesystem. Missing files are fine; -exec runs only on matches.
sed_targets=()
if [ -d "$NEW_HOME" ]; then
  while IFS= read -r -d '' f; do sed_targets+=("$f"); done < <(
    find "$NEW_HOME" -maxdepth 3 -type f \
      \( -name '.*rc' -o -name '.profile' -o -name '.bash_profile' -o -name '.zprofile' -o -name '.zshenv' -o -name '.login' \) \
      -print0 2>/dev/null
  )
fi
if [ -d /etc/profile.d ]; then
  while IFS= read -r -d '' f; do sed_targets+=("$f"); done < <(
    find /etc/profile.d -maxdepth 1 -type f -name '*.sh' -print0 2>/dev/null
  )
fi
if [ ${#sed_targets[@]} -gt 0 ]; then
  # Escape slashes in paths for sed's s|…|…| delimiter (which we already
  # use) — paths can't contain |, so this is safe.
  sed -i "s|$OLD_HOME|$NEW_HOME|g" "${sed_targets[@]}" || true
fi

# Sudoers drop-in: rename file and rewrite the username inside.
if [ -f "/etc/sudoers.d/$OLD_USER" ]; then
  sed -i "s/\\b$OLD_USER\\b/$NEW_USER/g" "/etc/sudoers.d/$OLD_USER"
  mv "/etc/sudoers.d/$OLD_USER" "/etc/sudoers.d/$NEW_USER"
  chmod 0440 "/etc/sudoers.d/$NEW_USER"
fi
ensure_sudo_file "$NEW_USER"

log "normalised: $NEW_USER uid=$(id -u "$NEW_USER") home=$NEW_HOME"
