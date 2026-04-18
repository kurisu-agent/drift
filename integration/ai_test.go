//go:build integration

package integration_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kurisu-agent/drift/integration"
)

// TestAICommand verifies that `drift ai` ssh's into the circuit and runs
// `claude --dangerously-skip-permissions` from $HOME/.drift, with the
// CLAUDE.md that `lakitu init` dropped sitting in that cwd.
//
// `claude` on the circuit is a shim that records its argv and cwd to a log
// file and exits 0 — we never need the real binary for this path. `--ssh`
// forces the plain ssh branch so mosh doesn't need to be installed in the
// test container.
func TestAICommand(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	c := integration.StartCircuit(ctx, t)
	if err := integration.SSHCommand(ctx, c, "lakitu", "init"); err != nil {
		t.Fatalf("lakitu init: %v", err)
	}

	// Install a claude shim on the circuit's PATH. Writes its cwd + every
	// argv entry as one line to /tmp/claude.log and exits 0 so `drift ai`
	// sees a clean session exit.
	c.InstallBin(ctx, "claude", `#!/bin/sh
{
  pwd
  printf '%s\n' "$0" "$@"
} > /tmp/claude.log
chmod 0666 /tmp/claude.log
exit 0
`)

	c.RegisterCircuit(ctx, "test")

	_, stderr, code := c.Drift(ctx, "ai", "--ssh")
	if code != 0 {
		t.Fatalf("drift ai: exit=%d stderr=%q", code, stderr)
	}

	out := string(c.ExecInContainer(ctx, "cat", "/tmp/claude.log"))
	got := strings.TrimSpace(out)
	if got == "" {
		t.Fatalf("claude shim was never invoked (empty log)\nstderr=%s", stderr)
	}
	// First line is the cwd; remaining lines are argv ($0, $1, …).
	lines := strings.Split(got, "\n")
	home := "/home/" + c.User
	wantCwd := home + "/.drift"
	if lines[0] != wantCwd {
		t.Errorf("claude cwd = %q, want %q\nlog=%q", lines[0], wantCwd, got)
	}
	// argv[1:] — the shim logs one line per arg. Expect exactly the
	// --dangerously-skip-permissions flag.
	joined := strings.Join(lines[2:], " ")
	if !strings.Contains(joined, "--dangerously-skip-permissions") {
		t.Errorf("claude argv missing --dangerously-skip-permissions: %q", joined)
	}

	// CLAUDE.md was written by lakitu init and should be readable by the
	// user from the same directory claude is launched in.
	mdOut := string(c.ExecInContainer(ctx, "cat", wantCwd+"/CLAUDE.md"))
	if !strings.Contains(mdOut, "circuit — agent context") {
		t.Errorf("CLAUDE.md missing expected header:\n%s", mdOut)
	}
}

