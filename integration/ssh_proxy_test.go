//go:build integration

package integration_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kurisu-agent/drift/integration"
)

// TestSSHProxyEchoOK covers plans/TODO.md § Phase 11's "ssh
// drift.<circuit>.<kart> echo ok" smoke: the client invokes OpenSSH with the
// harness-generated ssh_config whose `Host drift.*.*` block routes through
// `drift ssh-proxy %h %p`. drift ssh-proxy then opens a nested SSH
// connection to the circuit and runs `devpod ssh <kart> --stdio`.
//
// Because devpod requires docker-in-docker (not available in the
// integration image), the test replaces /usr/local/bin/devpod with a shim
// that pipes stdin/stdout to local sshd via netcat. Client's SSH handshake
// therefore tunnels over the ProxyCommand pipe and terminates against the
// same sshd — a convenient stand-in for the real `devpod ssh --stdio` path
// that still exercises the full drift+OpenSSH plumbing end-to-end.
func TestSSHProxyEchoOK(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	c := integration.StartCircuit(ctx, t)
	if err := integration.SSHCommand(ctx, c, "lakitu", "init"); err != nil {
		t.Fatalf("lakitu init: %v", err)
	}
	c.RegisterCircuit(ctx, "test")

	// Shim pipes stdio to local sshd. Output buffering is disabled so the
	// outer client sees SSH banner bytes promptly.
	c.InstallDevpodShim(ctx, `#!/bin/sh
# devpod shim for the ssh-proxy integration test. drift ssh-proxy invokes
# us as `+"`devpod ssh <kart> --stdio`"+` over an outer SSH session; we
# bridge stdio to local sshd on port 22 so the client's nested SSH
# handshake terminates against a real server.
exec nc -q0 localhost 22
`)

	stdout, stderr, code := c.SSH(ctx, "drift.test.mykart", "echo", "ok")
	if code != 0 {
		t.Fatalf("ssh drift.test.mykart echo ok: exit=%d\nstdout=%q\nstderr=%q", code, stdout, stderr)
	}
	if strings.TrimSpace(stdout) != "ok" {
		t.Fatalf("stdout = %q, want 'ok'", stdout)
	}
}
