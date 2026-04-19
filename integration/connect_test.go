//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kurisu-agent/drift/integration"
)

// TestDriftConnectSSH exercises the full `drift connect --ssh <kart>`
// pipeline: kart.info over RPC, ssh through the managed drift.<circuit>
// alias, then `devpod ssh <kart>` on the circuit. devpod is replaced with a
// shim that pretends the kart exists and is running, and that `devpod ssh
// <kart>` is a one-shot command printing a sentinel. A real PTY isn't
// wired up in the harness, so ssh -t emits its "Pseudo-terminal will not be
// allocated" warning to stderr and we match the sentinel on stdout instead.
//
// mosh is not available in the circuit or the devcontainer, so --ssh is the
// natural choice here. --no-mosh isn't needed because the CLI's mosh
// detection runs against the drift-side PATH and LookPath returns ENOENT;
// --ssh makes the intent explicit and pins the argv shape.
func TestDriftConnectSSH(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	c := integration.StartCircuit(ctx, t)
	if err := integration.SSHCommand(ctx, c, "lakitu", "init"); err != nil {
		t.Fatalf("lakitu init: %v", err)
	}
	c.RegisterCircuit(ctx, "test")

	kart := c.KartName("conn")

	// Shim covers only the devpod calls connect's pipeline makes:
	//   * list   → single-entry, so kart.info can resolve the kart.
	//   * status → Running, so connect skips kart.start.
	//   * ssh    → prints a sentinel and exits 0 so ssh -t unblocks quickly.
	// Any other subcommand succeeds silently; unused branches cost one line.
	shim := fmt.Sprintf(`#!/bin/sh
case "$1" in
  list)
    printf '[{"id":"%[1]s","source":{"gitRepository":"u"},"provider":{"name":"docker"}}]\n'
    ;;
  status)
    printf '{"state":"Running"}\n'
    ;;
  ssh)
    echo connect-shim-ok
    ;;
esac
exit 0
`, kart)
	c.InstallDevpodShim(ctx, shim)

	// kart.info also reads garage/karts/<name>/config.yaml to classify the
	// devpod workspace as drift-managed (vs. a stale entry). Write a
	// minimal config so the handler doesn't treat the kart as stale.
	writeCfg := fmt.Sprintf(`set -e
mkdir -p ~/.drift/garage/karts/%[1]s
cat > ~/.drift/garage/karts/%[1]s/config.yaml <<'EOF'
repo: u
source_mode: clone
created_at: "2026-04-19T00:00:00Z"
EOF
`, kart)
	if err := integration.SSHCommand(ctx, c, "sh", "-c", writeCfg); err != nil {
		t.Fatalf("write kart config: %v", err)
	}

	stdout, stderr, code := c.Drift(ctx, "connect", "--ssh", kart)
	if code != 0 {
		t.Fatalf("drift connect --ssh %s: exit=%d\nstdout=%q\nstderr=%q", kart, code, stdout, stderr)
	}
	if !strings.Contains(stdout, "connect-shim-ok") {
		t.Fatalf("stdout = %q, want substring 'connect-shim-ok' (stderr=%q)", stdout, stderr)
	}
}
