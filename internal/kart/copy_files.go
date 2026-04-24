package kart

import (
	"fmt"
	"os"
	"strings"

	"github.com/kurisu-agent/drift/internal/model"
)

// copyFragment realises every `type: copy` entry in mounts as a shell
// fragment that writes each file into the container. Source is read
// from the lakitu host and base64-encoded for wire-safe transport.
// Missing source files are a user-facing error — nothing is written.
func copyFragment(mounts []model.Mount) (string, error) {
	copies := filterCopyMounts(mounts)
	if len(copies) == 0 {
		return "", nil
	}
	var b strings.Builder
	for _, m := range copies {
		src := expandHomeTildeSource(m.Source)
		data, err := os.ReadFile(src)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", src, err)
		}
		dst := copyTargetExpand(m.Target)
		fmt.Fprintf(&b, `mkdir -p "$(dirname %q)"`+"\n", dst)
		b.WriteString(base64WriteStmt(dst, data))
	}
	return b.String(), nil
}

func filterCopyMounts(mounts []model.Mount) []model.Mount {
	var out []model.Mount
	for _, m := range mounts {
		if isCopyMount(m) && strings.TrimSpace(m.Target) != "" {
			out = append(out, m)
		}
	}
	return out
}

func copyTargetExpand(target string) string {
	if suffix, ok := targetInHome(target); ok {
		if suffix == "" {
			return "$HOME"
		}
		return "$HOME/" + suffix
	}
	return target
}
