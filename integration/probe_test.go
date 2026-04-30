//go:build integration

package integration_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kurisu-agent/drift/integration"
)

// TestCircuitAddProbe verifies the server.version probe path end-to-end:
// drift opens an ssh connection to the circuit, runs `lakitu rpc`, and
// decodes the version envelope. The JSON output format is asserted so a
// regression in the probe_result shape fails loudly.
func TestCircuitAddProbe(t *testing.T) {
	ctx := integration.TestCtx(t, 5*time.Minute)

	c := integration.StartCircuit(ctx, t)
	if err := integration.SSHCommand(ctx, c, "lakitu", "init"); err != nil {
		t.Fatalf("lakitu init: %v", err)
	}
	if err := integration.SSHCommand(ctx, c, "lakitu", "config", "set", "name", integration.CircuitName); err != nil {
		t.Fatalf("lakitu config set name: %v", err)
	}

	// `circuit add` now probes unconditionally — the probe is how we
	// learn the canonical name. The JSON payload carries the lakitu
	// version + API rather than a separate probe object.
	stdout, stderr, code := c.Drift(ctx, "--output", "json",
		"circuit", "add", c.Target(),
		"--no-ssh-config",
	)
	if code != 0 {
		t.Fatalf("drift circuit add: exit=%d stderr=%q stdout=%q", code, stderr, stdout)
	}

	var payload struct {
		Circuit string `json:"circuit"`
		Host    string `json:"host"`
		Lakitu  string `json:"lakitu_version"`
		API     int    `json:"api"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &payload); err != nil {
		t.Fatalf("decode add json: %v\nstdout=%s", err, stdout)
	}
	if payload.Circuit != integration.CircuitName {
		t.Errorf("circuit = %q, want %q (server-advertised name)", payload.Circuit, integration.CircuitName)
	}
	if payload.Lakitu == "" {
		t.Errorf("lakitu_version is empty")
	}
	if payload.API <= 0 {
		t.Errorf("api = %d, want > 0", payload.API)
	}
}
