#!/usr/bin/env bash
# Check that the hand-curated `drift help` (rendered by
# internal/cli/drift/help.go's driftHelpSections) still covers every
# leaf command in the Kong-derived `drift help --full` catalog. If a
# new subcommand was added without a line in the curated sections,
# this script flags it so the human can update help.go.
#
# lakitu help is auto-derived via clihelp.Render, so only drift needs
# this curation check.

set -euo pipefail

GO="${GO:-}"
if [[ -z "$GO" ]]; then
  if command -v go >/dev/null 2>&1; then
    GO=go
  else
    GO=$(ls /nix/store/*-go-*/bin/go 2>/dev/null | head -1 || true)
  fi
fi
if [[ -z "$GO" || ! -x "$GO" ]]; then
  echo "verify_help.sh: could not locate 'go' on PATH or under /nix/store" >&2
  exit 1
fi

repo_root=$(git rev-parse --show-toplevel 2>/dev/null || pwd)
cd "$repo_root"

full=$("$GO" run ./cmd/drift help --full 2>&1 || true)
curated=$("$GO" run ./cmd/drift help 2>&1 || true)

# Extract leaf command paths from `drift help --full`. The COMMANDS
# block lists each leaf as "  <path> — <description>".
mapfile -t leaves < <(
  printf '%s\n' "$full" \
    | awk '
        /^COMMANDS/ { in_cmds = 1; next }
        in_cmds && /^[A-Z]/ { in_cmds = 0 }
        in_cmds && /^  [a-z]/ {
          # Everything before the em-dash is the path. Fall back to
          # first field if no dash (shouldnt happen with current Kong
          # output).
          idx = index($0, "—")
          if (idx == 0) { print $1; next }
          path = substr($0, 1, idx - 1)
          gsub(/^  /, "", path)
          gsub(/[[:space:]]+$/, "", path)
          print path
        }
      '
)

# Commands we intentionally skip from the curation check: "help"
# itself is obvious and "update" is a maintenance verb — both are
# either listed or don't need surfacing in day-one help.
skip_leaf() {
  case "$1" in
    help) return 0 ;;  # help is always implied
    *) return 1 ;;
  esac
}

missing=()
for leaf in "${leaves[@]}"; do
  if skip_leaf "$leaf"; then continue; fi
  # A leaf is considered "covered" if every whitespace-separated
  # token of its path appears as a word somewhere in the curated
  # output. This matches shorthand like "circuit set default|name":
  # the tokens circuit, set, default all appear.
  uncovered=0
  for tok in $leaf; do
    if ! grep -qwF -- "$tok" <<<"$curated"; then
      uncovered=1
      break
    fi
  done
  if (( uncovered == 1 )); then
    missing+=("$leaf")
  fi
done

echo "=== drift help --full leaves ==="
printf '  %s\n' "${leaves[@]}"
echo
echo "=== drift help (curated) ==="
printf '%s\n' "$curated"
echo

if (( ${#missing[@]} == 0 )); then
  echo "OK: every leaf command has at least one token present in the curated help."
  echo "(Token coverage is coarse — still eyeball the curated output for stale rows."
  echo " If a curated line refers to a renamed/removed command, fix it by hand.)"
else
  echo "MISSING from curated drift help (fix driftHelpSections in"
  echo "internal/cli/drift/help.go):"
  printf '  %s\n' "${missing[@]}"
  exit 1
fi
