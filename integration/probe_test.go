//go:build integration

package integration_test

import (
	"context"
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	c := integration.StartCircuit(ctx, t)
	if err := integration.SSHCommand(ctx, c, "lakitu", "init"); err != nil {
		t.Fatalf("lakitu init: %v", err)
	}

	// --no-ssh-config keeps drift's ssh_config writer out of the way; the
	// harness already wired Host drift.* in $HOME/.ssh/config. The probe
	// then shells out to `ssh drift.test lakitu rpc`, which hits the real
	// server.version handler.
	stdout, stderr, code := c.Drift(ctx, "--output", "json",
		"circuit", "add", "test",
		"--host", c.Target(),
		"--no-ssh-config",
	)
	if code != 0 {
		t.Fatalf("drift circuit add: exit=%d stderr=%q stdout=%q", code, stderr, stdout)
	}

	var payload struct {
		Circuit    string `json:"circuit"`
		ProbeError string `json:"probe_error"`
		Probe      *struct {
			Version   string `json:"version"`
			API       int    `json:"api"`
			LatencyMS int64  `json:"latency_ms"`
		} `json:"probe"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &payload); err != nil {
		t.Fatalf("decode add json: %v\nstdout=%s", err, stdout)
	}
	if payload.ProbeError != "" {
		t.Fatalf("probe_error = %q (want empty)\nstderr=%s", payload.ProbeError, stderr)
	}
	if payload.Probe == nil {
		t.Fatalf("probe result missing from payload: %s", stdout)
	}
	if payload.Probe.Version == "" {
		t.Errorf("probe.version is empty")
	}
	if payload.Probe.API <= 0 {
		t.Errorf("probe.api = %d, want > 0", payload.Probe.API)
	}
}
