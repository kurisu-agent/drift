package kart

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/kurisu-agent/drift/internal/model"
)

// homeMountSymlinks picks out the bind mounts whose target is a `~/`-
// form and renders them as suffix strings the post-up helper can act
// on. `type: copy` entries are skipped — copy-into-container happens
// via copyFragment separately. Bare `~` is dropped — symlinking
// $HOME onto a mount makes no sense.
func homeMountSymlinks(mounts []model.Mount) []string {
	seen := make(map[string]bool, len(mounts))
	var out []string
	for _, m := range mounts {
		if isCopyMount(m) {
			continue
		}
		suffix, ok := targetInHome(m.Target)
		if !ok || suffix == "" {
			continue
		}
		if seen[suffix] {
			continue
		}
		seen[suffix] = true
		out = append(out, suffix)
	}
	return out
}

// symlinkFragment returns the shell snippet that realises each
// `~/<suffix>` bind mount as `$HOME/<suffix> → /mnt/lakitu-host/<suffix>`
// inside the container. If target already exists as a non-symlink
// (e.g. a devcontainer feature pre-created `~/.claude`), it's moved
// aside with a timestamp suffix rather than clobbered — recoverable
// from inside the container with `mv <path>.lakitu-bak.<stamp>
// <path>`.
func symlinkFragment(suffixes []string) string {
	if len(suffixes) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`stamp=$(date +%s)` + "\n")
	for _, suffix := range suffixes {
		dst := "$HOME/" + suffix
		src := hostMountPrefix + suffix
		if dir := filepath.Dir(suffix); dir != "" && dir != "." {
			fmt.Fprintf(&b, `mkdir -p "$HOME/%s"`+"\n", dir)
		}
		fmt.Fprintf(&b, `if [ -e %q ] && [ ! -L %q ]; then mv %q "%s.lakitu-bak.$stamp"; fi`+"\n", dst, dst, dst, dst)
		fmt.Fprintf(&b, `ln -sfn %q %q`+"\n", src, dst)
	}
	return b.String()
}
