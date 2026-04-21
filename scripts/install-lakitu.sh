#!/bin/sh
# lakitu circuit installer. Piped from raw.githubusercontent.com:
#   curl -fsSL https://raw.githubusercontent.com/kurisu-agent/drift/main/scripts/install-lakitu.sh | sh
#
# Installs lakitu on a Linux host, wires up systemd user lingering + the
# per-kart template unit, adds the current user to the docker group, and
# runs `lakitu init` to bootstrap ~/.drift/garage/. lakitu downloads its
# pinned devpod binary on first run (SHA-verified into ~/.drift/bin/), so
# there is nothing to install beyond lakitu itself.
#
# Env overrides:
#   LAKITU_VERSION    tag to install (default: latest)
#   LAKITU_REPO       owner/repo (default: kurisu-agent/drift)
#   LAKITU_SKIP_INIT  set non-empty to skip the final `lakitu init` call
#   LAKITU_SKIP_MOSH  set non-empty to skip `apt-get install mosh`

INSTALLER_VERSION=1

set -eu

REPO="${LAKITU_REPO:-kurisu-agent/drift}"
VERSION="${LAKITU_VERSION:-latest}"

log() { printf 'lakitu-install: %s\n' "$*" >&2; }
die() { log "error: $*"; exit 1; }

log "installer v${INSTALLER_VERSION}"

command -v curl >/dev/null 2>&1 || die "curl is required"
command -v tar  >/dev/null 2>&1 || die "tar is required"

# sudo is only required when we aren't already root. Resolved once up
# front so the rest of the script can prefix every privileged step
# uniformly with $SUDO.
SUDO=""
if [ "$(id -u)" -ne 0 ]; then
  command -v sudo >/dev/null 2>&1 || die "sudo is required (or run this script as root)"
  SUDO=sudo
fi

uname_s=$(uname -s)
uname_m=$(uname -m)
[ "$uname_s" = "Linux" ] || die "lakitu runs on Linux only; got $uname_s"
case "$uname_m" in
  x86_64|amd64)  goarch=amd64 ;;
  aarch64|arm64) goarch=arm64 ;;
  *)             die "unsupported arch: $uname_m" ;;
esac

if [ "$VERSION" = "latest" ]; then
  api="https://api.github.com/repos/${REPO}/releases/latest"
else
  api="https://api.github.com/repos/${REPO}/releases/tags/${VERSION}"
fi

suffix="_linux_${goarch}.tar.gz"
log "fetching release metadata ($REPO $VERSION, linux/$goarch)"
asset_url=$(curl -fsSL "$api" \
  | grep -Eo '"browser_download_url": *"[^"]+"' \
  | sed -E 's/.*"(https[^"]+)"/\1/' \
  | grep -E "/lakitu_[^/]+${suffix}$" \
  | head -n1 || true)
[ -n "$asset_url" ] || die "no lakitu asset found for linux/${goarch} in ${VERSION}"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

log "downloading $asset_url"
curl -fsSL "$asset_url" -o "$tmp/lakitu.tar.gz"
tar -xzf "$tmp/lakitu.tar.gz" -C "$tmp" lakitu
$SUDO install -m 0755 "$tmp/lakitu" /usr/local/bin/lakitu
log "installed /usr/local/bin/lakitu"

# Docker group — noop if already a member. lakitu needs docker access
# to drive devpod's docker provider.
if getent group docker >/dev/null 2>&1; then
  if ! id -nG "$USER" 2>/dev/null | tr ' ' '\n' | grep -qx docker; then
    log "adding $USER to docker group (log out and back in to take effect)"
    $SUDO usermod -aG docker "$USER"
  fi
else
  log "note: no 'docker' group found — install Docker first, then rerun"
fi

# Mosh — optional. apt-get only, since that's the common circuit OS
# (Debian/Ubuntu). Anything else, operator installs manually.
if [ -z "${LAKITU_SKIP_MOSH:-}" ] && ! command -v mosh-server >/dev/null 2>&1; then
  if command -v apt-get >/dev/null 2>&1; then
    log "installing mosh (resilient shells; set LAKITU_SKIP_MOSH=1 to skip)"
    $SUDO apt-get update -qq
    $SUDO apt-get install -y mosh
  else
    log "note: mosh missing and apt-get unavailable — install it manually for resilient shells"
  fi
fi

# Systemd user lingering — without this, the user's systemd instance
# dies on logout and lakitu-kart@*.service units stop with it.
if command -v loginctl >/dev/null 2>&1; then
  $SUDO loginctl enable-linger "$USER"
fi

# Per-kart systemd template. Fetched from the same ref the installer
# itself came from so versions stay consistent.
mkdir -p "$HOME/.config/systemd/user"
unit_url="https://raw.githubusercontent.com/${REPO}/main/packaging/systemd/lakitu-kart@.service"
log "fetching $unit_url"
curl -fsSL "$unit_url" > "$HOME/.config/systemd/user/lakitu-kart@.service"
log "installed $HOME/.config/systemd/user/lakitu-kart@.service"

if [ -z "${LAKITU_SKIP_INIT:-}" ]; then
  log "running 'lakitu init' (also triggers the first pinned-devpod download)"
  /usr/local/bin/lakitu init
else
  log "skipping 'lakitu init' (LAKITU_SKIP_INIT set) — run it manually when ready"
fi

log "done. 'lakitu --help' for commands."
