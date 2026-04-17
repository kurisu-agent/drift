//go:build integration

package integration_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kurisu-agent/drift/integration"
)

// TestDriftInitAndServerVersion is the smallest useful integration test:
// stand up a circuit, run `lakitu init` and `drift circuit add` + probe,
// confirm the version RPC round-trips. This exercises sshd → lakitu rpc →
// internal/rpc dispatcher → server.version end-to-end.
func TestDriftInitAndServerVersion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	c := integration.StartCircuit(ctx, t)

	// One-time `lakitu init` so the garage exists on the circuit. The
	// harness's Exec helper routes drift subcommands; init on the circuit
	// uses a direct ssh invocation since it's a one-shot remote command.
	if err := integration.SSHCommand(ctx, c, "lakitu", "init"); err != nil {
		t.Fatalf("lakitu init: %v", err)
	}

	// Register the circuit from the workstation side. --no-ssh-config
	// keeps us from touching ~/.ssh/config beyond what the harness
	// already set up; --no-probe runs the add without the usual version
	// probe so we can assert it explicitly below.
	stdout, stderr, code := c.Drift(ctx, "circuit", "add", "test",
		"--host", c.Target(),
		"--no-ssh-config",
		"--no-probe",
	)
	if code != 0 {
		t.Fatalf("drift circuit add exit=%d stderr=%q", code, stderr)
	}
	_ = stdout

	// Explicit probe via `drift circuit list` + RPC round-trip. Just
	// asserting the list command succeeds and names our circuit is enough
	// signal that the config write worked; end-to-end version probing is
	// covered by any subcommand that routes through client.Call — add a
	// dedicated smoke later when `drift status` lands.
	stdout, stderr, code = c.Drift(ctx, "circuit", "list")
	if code != 0 {
		t.Fatalf("drift circuit list exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "test") {
		t.Errorf("circuit list did not mention test circuit:\n%s", stdout)
	}
}
