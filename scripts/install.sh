#!/bin/sh
# drift client installer. Piped from raw.githubusercontent.com:
#   curl -fsSL https://raw.githubusercontent.com/kurisu-agent/drift/main/scripts/install.sh | sh
#
# Env overrides:
#   DRIFT_INSTALL_DIR   target dir (default: $HOME/.local/bin, or /usr/local/bin if root, or $PREFIX/bin on Termux)
#   DRIFT_VERSION       tag to install (default: latest)
#   DRIFT_REPO          owner/repo (default: kurisu-agent/drift)

# Bumped on every installer-script change. Gets logged up front so a stale
# raw.githubusercontent.com cache is visible to the user ("I got v3 but the
# repo's on v5 — wait a few minutes and retry").
INSTALLER_VERSION=3

set -eu

REPO="${DRIFT_REPO:-kurisu-agent/drift}"
VERSION="${DRIFT_VERSION:-latest}"

log() { printf 'drift-install: %s\n' "$*" >&2; }
die() { log "error: $*"; exit 1; }

log "installer v${INSTALLER_VERSION}"

command -v curl >/dev/null 2>&1 || die "curl is required"
command -v tar  >/dev/null 2>&1 || die "tar is required"

uname_s=$(uname -s)
uname_m=$(uname -m)
case "$uname_s" in
  Linux)   goos=linux ;;
  Darwin)  goos=darwin ;;
  *)       die "unsupported OS: $uname_s" ;;
esac
case "$uname_m" in
  x86_64|amd64)  goarch=amd64 ;;
  aarch64|arm64) goarch=arm64 ;;
  *)             die "unsupported arch: $uname_m" ;;
esac

# Termux exposes itself as Linux but prefers the android/arm64 asset, and
# $PREFIX/bin is always on PATH there — so install to it by default to
# avoid the ~/.local/bin PATH dance. Detection via TERMUX_VERSION (always
# exported) or PREFIX pointing inside com.termux (set in every Termux shell).
is_termux=0
case "${PREFIX:-}" in
  */com.termux/*) is_termux=1 ;;
esac
if [ -n "${TERMUX_VERSION:-}" ]; then
  is_termux=1
fi
if [ "$is_termux" -eq 1 ]; then
  goos=android
fi

if [ "$VERSION" = "latest" ]; then
  api="https://api.github.com/repos/${REPO}/releases/latest"
else
  api="https://api.github.com/repos/${REPO}/releases/tags/${VERSION}"
fi

# Pull the matching asset URL out of the release JSON. Avoids a jq dep by
# grep-extracting the browser_download_url field whose surrounding name ends
# in _<goos>_<goarch>.tar.gz.
suffix="_${goos}_${goarch}.tar.gz"
log "fetching release metadata ($REPO $VERSION, $goos/$goarch)"
asset_url=$(curl -fsSL "$api" \
  | grep -Eo '"browser_download_url": *"[^"]+"' \
  | sed -E 's/.*"(https[^"]+)"/\1/' \
  | grep -E "/drift_[^/]+${suffix}$" \
  | head -n1 || true)

[ -n "$asset_url" ] || die "no drift asset found for ${goos}/${goarch} in ${VERSION}"

if [ -z "${DRIFT_INSTALL_DIR:-}" ]; then
  if [ "$is_termux" -eq 1 ] && [ -n "${PREFIX:-}" ]; then
    DRIFT_INSTALL_DIR="${PREFIX}/bin"
  elif [ "$(id -u)" -eq 0 ]; then
    DRIFT_INSTALL_DIR=/usr/local/bin
  else
    DRIFT_INSTALL_DIR="${HOME}/.local/bin"
  fi
fi
mkdir -p "$DRIFT_INSTALL_DIR"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

log "downloading $asset_url"
curl -fsSL "$asset_url" -o "$tmp/drift.tar.gz"
tar -xzf "$tmp/drift.tar.gz" -C "$tmp" drift
install -m 0755 "$tmp/drift" "$DRIFT_INSTALL_DIR/drift"

log "installed $DRIFT_INSTALL_DIR/drift"
case ":$PATH:" in
  *":$DRIFT_INSTALL_DIR:"*) ;;
  *) log "note: $DRIFT_INSTALL_DIR is not on PATH — add it to your shell rc." ;;
esac
"$DRIFT_INSTALL_DIR/drift" --version 2>/dev/null || true
