package kart

import (
	_ "embed"
	"fmt"
	"strings"
)

// normaliseUserScript is the bash script that renames the image's default
// non-root user to the character's name (or creates one fresh when the
// image ships without a uid-1000 user). Must run as root; the wrapper
// in normaliseUserWrapper elevates via sudo when needed.
// Idempotent — re-runs against an already-normalised container are
// no-ops.
//
//go:embed normalize-user.sh
var normaliseUserScript string

// normaliseUserOnCreateKey is the object-form onCreateCommand key lakitu
// uses. Unique prefix keeps us from colliding with project-authored
// entries when devpod merges overlays.
const normaliseUserOnCreateKey = "lakitu-normalize-user"

// normaliseUserWrapperTemplate is the bash one-liner invoked as the
// onCreateCommand. It materialises the embedded script into a tmpfile
// and then executes it as root — directly if the container is already
// running as root, otherwise via `sudo -n` (passwordless sudo is a
// convention of all Microsoft devcontainer base images). devpod v0.22
// runs onCreateCommand as the image's default non-root user rather
// than root, which is why the elevation is required even though the
// devcontainer spec says otherwise.
//
// The heredoc uses a quoted terminator (`LAKITU_NORMALIZE_EOF`) so the
// script body passes through unexpanded; the body itself is inlined at
// splice time via fmt.Sprintf.
const normaliseUserWrapperTemplate = `set -e
cat >/tmp/lakitu-normalize-user.sh <<'LAKITU_NORMALIZE_EOF'
%s
LAKITU_NORMALIZE_EOF
if [ "$(id -u)" -eq 0 ]; then
  exec bash /tmp/lakitu-normalize-user.sh "$1"
fi
if ! command -v sudo >/dev/null 2>&1; then
  echo "normalize-user: non-root invocation and no sudo available; cannot elevate" >&2
  exit 4
fi
exec sudo -n bash /tmp/lakitu-normalize-user.sh "$1"
`

// normaliseUserWrapper returns the concrete onCreateCommand bash body,
// with the embedded script inlined via heredoc. The heredoc terminator
// collision is defended against: a script whose body happens to
// contain the exact EOF token would break the wrapper.
func normaliseUserWrapper() (string, error) {
	if strings.Contains(normaliseUserScript, "LAKITU_NORMALIZE_EOF") {
		return "", fmt.Errorf("normalize-user.sh contains the heredoc terminator; rename one of them")
	}
	return fmt.Sprintf(normaliseUserWrapperTemplate, normaliseUserScript), nil
}

// spliceUserNormalisation mutates root (the parsed devcontainer overlay)
// to set remoteUser and add the user-normalisation script as an
// object-form onCreateCommand entry. Existing onCreateCommand values are
// preserved — a string/array form is moved under the "project" key so
// the merged object form runs both.
func spliceUserNormalisation(root map[string]any, character string) error {
	if character == "" {
		return fmt.Errorf("character name is required for user normalisation")
	}
	root["remoteUser"] = character

	// Override HOME in the container env so tooling (npm, node, zsh,
	// …) that reads $HOME from the image's baked-in ENV directive
	// picks up the renamed home dir. Without this, /home/<old-user>
	// is gone but $HOME still points at it and postCreate hooks fail.
	// Merge with any existing containerEnv so project-side entries
	// survive.
	envBlock, _ := root["containerEnv"].(map[string]any)
	if envBlock == nil {
		envBlock = map[string]any{}
	}
	envBlock["HOME"] = "/home/" + character
	root["containerEnv"] = envBlock

	wrapper, err := normaliseUserWrapper()
	if err != nil {
		return err
	}

	// onCreateCommand: devcontainer spec allows string | []string | map[string]any.
	// Normalise to the object form so we can add our key without clobbering
	// a project-authored script.
	cmd := []any{"bash", "-c", wrapper, "normalize-user", character}
	merged := map[string]any{normaliseUserOnCreateKey: cmd}
	switch v := root["onCreateCommand"].(type) {
	case nil:
		// no existing entry
	case map[string]any:
		for k, existing := range v {
			if k == normaliseUserOnCreateKey {
				// Our key wins — re-running splice on a previously
				// spliced overlay stays idempotent.
				continue
			}
			merged[k] = existing
		}
	default:
		// string or []any — demote to the "project" key so both run.
		merged["project"] = v
	}
	root["onCreateCommand"] = merged
	return nil
}
