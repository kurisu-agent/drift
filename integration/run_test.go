//go:build integration

package integration_test

import (
	"strings"
	"testing"
	"time"

	"github.com/kurisu-agent/drift/integration"
)

// TestRun_Ai exercises `drift run ai` end-to-end against a fresh circuit:
//   - `lakitu init` seeds ~/.drift/runs.yaml with the built-in `ai` entry.
//   - `drift run ai --ssh` resolves the entry via run.resolve, then ssh's
//     in with the rendered command (`cd $HOME/.drift && exec claude …`).
//   - A shim `claude` records its cwd + argv so we can assert on both.
//
// --ssh forces the plain ssh branch (no mosh dependency in the test
// container).
func TestRun_Ai(t *testing.T) {
	ctx := integration.TestCtx(t, 5*time.Minute)

	c, _ := integration.StartReadyCircuit(ctx, t, false)

	c.InstallBin(ctx, "claude", `#!/bin/sh
{
  pwd
  printf '%s\n' "$0" "$@"
} > /tmp/claude.log
chmod 0666 /tmp/claude.log
exit 0
`)

	_, stderr, code := c.Drift(ctx, "run", "ai", "--ssh")
	if code != 0 {
		t.Fatalf("drift run ai: exit=%d stderr=%q", code, stderr)
	}

	out := string(c.ExecInContainer(ctx, "cat", "/tmp/claude.log"))
	got := strings.TrimSpace(out)
	if got == "" {
		t.Fatalf("claude shim was never invoked (empty log)\nstderr=%s", stderr)
	}
	lines := strings.Split(got, "\n")
	home := "/home/" + c.User
	wantCwd := home + "/.drift"
	if lines[0] != wantCwd {
		t.Errorf("claude cwd = %q, want %q\nlog=%q", lines[0], wantCwd, got)
	}
	joined := strings.Join(lines[2:], " ")
	if !strings.Contains(joined, "--dangerously-skip-permissions") {
		t.Errorf("claude argv missing --dangerously-skip-permissions: %q", joined)
	}

	mdOut := string(c.ExecInContainer(ctx, "cat", wantCwd+"/CLAUDE.md"))
	if !strings.Contains(mdOut, "circuit — agent context") {
		t.Errorf("CLAUDE.md missing expected header:\n%s", mdOut)
	}
}

// TestRun_ListsBuiltins asserts that `drift runs` returns the entries
// seeded by `lakitu init` — proving there is no embedded client-side
// knowledge of run names.
func TestRun_ListsBuiltins(t *testing.T) {
	ctx := integration.TestCtx(t, 2*time.Minute)

	c, _ := integration.StartReadyCircuit(ctx, t, false)

	stdout, stderr, code := c.Drift(ctx, "runs")
	if code != 0 {
		t.Fatalf("drift runs: exit=%d stderr=%q", code, stderr)
	}
	for _, name := range []string{"ai", "scaffolder", "ping", "uptime", "disk", "mem"} {
		if !strings.Contains(stdout, name) {
			t.Errorf("drift runs output missing %q:\n%s", name, stdout)
		}
	}
}

// TestRun_UnknownAfterEdit appends a new entry to the circuit's runs.yaml
// after the drift client was built, then asserts it appears in `drift runs`
// — the "no embedded list" guarantee we want to preserve as entries grow.
func TestRun_UnknownAfterEdit(t *testing.T) {
	ctx := integration.TestCtx(t, 2*time.Minute)

	c, _ := integration.StartReadyCircuit(ctx, t, false)

	extra := "\n  hello-from-test:\n    description: \"added after client was built\"\n    mode: output\n    command: 'echo hi'\n"
	if err := integration.SSHCommand(ctx, c, "sh", "-c",
		"printf '%s' "+shellSingleQuote(extra)+" >> \"$HOME/.drift/runs.yaml\""); err != nil {
		t.Fatalf("append runs.yaml: %v", err)
	}

	stdout, stderr, code := c.Drift(ctx, "runs")
	if code != 0 {
		t.Fatalf("drift runs: exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "hello-from-test") {
		t.Errorf("appended entry not visible to client:\n%s", stdout)
	}
}

func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
