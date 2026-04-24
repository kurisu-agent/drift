package kart

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/kurisu-agent/drift/internal/devpod"
)

// containerScript accumulates bash fragments so lakitu's post-`devpod
// up` finalisers (symlinks, copies, CLAUDE.md drop) coalesce into a
// single `devpod ssh --command` instead of one round trip per step.
// Each ssh handshake is ~150-400 ms on a local docker, more over
// wireguard to a remote circuit — three round trips land near a
// second.
type containerScript struct {
	tag       string
	fragments []string
}

func newContainerScript(tag string) *containerScript { return &containerScript{tag: tag} }

// Append adds one fragment. Empty strings are dropped so callers can
// unconditionally append a maybe-empty fragment.
func (s *containerScript) Append(fragment string) {
	if fragment != "" {
		s.fragments = append(s.fragments, fragment)
	}
}

// Empty reports whether Run would be a no-op.
func (s *containerScript) Empty() bool { return len(s.fragments) == 0 }

// Run sends the assembled script to `devpod ssh --command`. Prelude
// (strict mode + HOME guard) is prepended here, so fragments can
// assume `$HOME` is set.
func (s *containerScript) Run(ctx context.Context, dp *devpod.Client, kart string) error {
	if s.Empty() {
		return nil
	}
	var b strings.Builder
	b.WriteString("set -euo pipefail\n")
	fmt.Fprintf(&b, `if [ -z "${HOME:-}" ]; then echo "%s: HOME is empty" >&2; exit 1; fi`+"\n", s.tag)
	for _, f := range s.fragments {
		b.WriteString(f)
	}
	if _, err := dp.SSH(ctx, devpod.SSHOpts{Name: kart, Command: b.String()}); err != nil {
		return fmt.Errorf("devpod ssh: %w", err)
	}
	return nil
}

// base64WriteStmt emits a shell line that decodes data into dst. dst
// is quoted with %q so `$HOME`-rooted paths pass through bash's
// double-quote expansion; absolute paths also round-trip correctly.
// Go's %q does not escape `$`, so the variable expands as intended.
func base64WriteStmt(dst string, data []byte) string {
	encoded := base64.StdEncoding.EncodeToString(data)
	return fmt.Sprintf(`printf '%%s' %q | base64 -d > %q`+"\n", encoded, dst)
}
