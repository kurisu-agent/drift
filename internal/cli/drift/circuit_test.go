package drift

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestDeps wires the CLI deps at a tempdir and returns a pointer to a
// captured probe record so tests can assert on it.
func newTestDeps(t *testing.T, probeFn func(ctx context.Context, circuit string) (*probeResult, error)) (deps, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	// drift's config pathing honors XDG_CONFIG_HOME; unset to force the HOME
	// fallback so the test owns the layout.
	t.Setenv("XDG_CONFIG_HOME", "")
	cfgPath := filepath.Join(root, ".config", "drift", "config.yaml")
	return deps{
		clientConfigPath: func() (string, error) { return cfgPath, nil },
		probe:            probeFn,
	}, root
}

func TestRunCircuitAdd_ProbeSuccessSurfacesLatency(t *testing.T) {
	d, home := newTestDeps(t, func(_ context.Context, circuit string) (*probeResult, error) {
		if circuit != "c1" {
			t.Errorf("probe got circuit %q, want c1", circuit)
		}
		return &probeResult{Version: "1.2.3", API: 1, Latency: 12 * time.Millisecond, LatencyMS: 12}, nil
	})
	var out, errBuf bytes.Buffer
	io := IO{Stdout: &out, Stderr: &errBuf}

	cli := &CLI{}
	cmd := circuitAddCmd{Name: "c1", Host: "dev@c1.example.com"}
	if rc := runCircuitAdd(context.Background(), io, cli, cmd, d); rc != 0 {
		t.Fatalf("runCircuitAdd rc=%d stderr=%s", rc, errBuf.String())
	}
	if !strings.Contains(out.String(), "probe ok — lakitu 1.2.3 (api 1, 12ms)") {
		t.Errorf("stdout missing probe summary: %q", out.String())
	}
	// SSH config was written under $HOME.
	if _, err := os.Stat(filepath.Join(home, ".config", "drift", "ssh_config")); err != nil {
		t.Errorf("managed ssh_config: %v", err)
	}
}

func TestRunCircuitAdd_ProbeFailureWarnsButKeepsCircuit(t *testing.T) {
	d, home := newTestDeps(t, func(_ context.Context, _ string) (*probeResult, error) {
		return nil, errors.New("boom")
	})
	var out, errBuf bytes.Buffer
	io := IO{Stdout: &out, Stderr: &errBuf}

	cli := &CLI{}
	if rc := runCircuitAdd(context.Background(), io, cli, circuitAddCmd{Name: "c1", Host: "dev@c1.example.com"}, d); rc != 0 {
		t.Fatalf("runCircuitAdd rc=%d stderr=%s", rc, errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "warning: probe failed: boom") {
		t.Errorf("stderr missing probe warning: %q", errBuf.String())
	}
	// Config and ssh_config are written regardless of probe outcome.
	if _, err := os.Stat(filepath.Join(home, ".config", "drift", "config.yaml")); err != nil {
		t.Errorf("client config.yaml: %v", err)
	}
}

func TestRunCircuitAdd_NoSSHConfigSkipsSSHWrites(t *testing.T) {
	d, home := newTestDeps(t, nil)
	var out, errBuf bytes.Buffer
	io := IO{Stdout: &out, Stderr: &errBuf}

	cli := &CLI{}
	cmd := circuitAddCmd{Name: "c1", Host: "dev@c1.example.com", NoSSHConfig: true, NoProbe: true}
	if rc := runCircuitAdd(context.Background(), io, cli, cmd, d); rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, errBuf.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "drift", "ssh_config")); !os.IsNotExist(err) {
		t.Errorf("expected managed ssh_config absent, got err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".ssh", "config")); !os.IsNotExist(err) {
		t.Errorf("expected user ssh config absent, got err=%v", err)
	}
}

func TestRunCircuitAdd_InvalidNameReturnsUserError(t *testing.T) {
	d, _ := newTestDeps(t, nil)
	var out, errBuf bytes.Buffer
	io := IO{Stdout: &out, Stderr: &errBuf}
	rc := runCircuitAdd(context.Background(), io, &CLI{}, circuitAddCmd{Name: "Bad_Name", Host: "x@y"}, d)
	if rc == 0 {
		t.Fatalf("expected non-zero rc, got 0; stderr=%s", errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "invalid") {
		t.Errorf("stderr = %q, want contains 'invalid'", errBuf.String())
	}
}

func TestRunCircuitList_JSONShape(t *testing.T) {
	d, _ := newTestDeps(t, nil)
	var out, errBuf bytes.Buffer
	io := IO{Stdout: &out, Stderr: &errBuf}

	// Seed a circuit.
	if rc := runCircuitAdd(context.Background(), io, &CLI{}, circuitAddCmd{
		Name: "c1", Host: "dev@c1", NoSSHConfig: true, NoProbe: true,
	}, d); rc != 0 {
		t.Fatalf("seed rc=%d", rc)
	}
	out.Reset()
	errBuf.Reset()

	cli := &CLI{Output: "json"}
	if rc := runCircuitList(io, cli, d); rc != 0 {
		t.Fatalf("list rc=%d stderr=%s", rc, errBuf.String())
	}
	var payload struct {
		Circuits []struct {
			Name    string `json:"name"`
			Host    string `json:"host"`
			Default bool   `json:"default"`
		} `json:"circuits"`
		Default string `json:"default_circuit"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v — body=%s", err, out.String())
	}
	if len(payload.Circuits) != 1 || payload.Circuits[0].Name != "c1" {
		t.Errorf("unexpected circuits: %+v", payload)
	}
	if payload.Default != "c1" {
		t.Errorf("default = %q, want c1", payload.Default)
	}
}

func TestRunCircuitRm_RemovesSSHBlockPreservesInclude(t *testing.T) {
	d, home := newTestDeps(t, nil)
	var out, errBuf bytes.Buffer
	io := IO{Stdout: &out, Stderr: &errBuf}

	for _, n := range []string{"a", "b"} {
		if rc := runCircuitAdd(context.Background(), io, &CLI{}, circuitAddCmd{
			Name: n, Host: "dev@" + n + ".example.com", NoProbe: true,
		}, d); rc != 0 {
			t.Fatalf("add %s rc=%d", n, rc)
		}
	}
	out.Reset()
	errBuf.Reset()

	if rc := runCircuitRm(io, &CLI{}, circuitRmCmd{Name: "a"}, d); rc != 0 {
		t.Fatalf("rm rc=%d stderr=%s", rc, errBuf.String())
	}
	mf, err := os.ReadFile(filepath.Join(home, ".config", "drift", "ssh_config"))
	if err != nil {
		t.Fatalf("read managed: %v", err)
	}
	if strings.Contains(string(mf), "Host drift.a\n") {
		t.Errorf("expected Host drift.a removed, got:\n%s", mf)
	}
	if !strings.Contains(string(mf), "Host drift.b\n") {
		t.Errorf("expected Host drift.b to remain, got:\n%s", mf)
	}
	uf, err := os.ReadFile(filepath.Join(home, ".ssh", "config"))
	if err != nil {
		t.Fatalf("read user: %v", err)
	}
	if !strings.Contains(string(uf), "Include ~/.config/drift/ssh_config") {
		t.Errorf("expected Include line to survive: %s", uf)
	}
}

func TestRunCircuitAdd_InvalidHostFormatReturnsError(t *testing.T) {
	d, _ := newTestDeps(t, nil)
	var out, errBuf bytes.Buffer
	io := IO{Stdout: &out, Stderr: &errBuf}
	rc := runCircuitAdd(context.Background(), io, &CLI{}, circuitAddCmd{
		Name: "c1", Host: "@nouser", NoProbe: true, NoSSHConfig: true,
	}, d)
	if rc == 0 {
		t.Fatalf("expected non-zero rc; stderr=%s", errBuf.String())
	}
}

func TestRunCircuitRm_UnknownCircuit(t *testing.T) {
	d, _ := newTestDeps(t, nil)
	var out, errBuf bytes.Buffer
	io := IO{Stdout: &out, Stderr: &errBuf}
	rc := runCircuitRm(io, &CLI{}, circuitRmCmd{Name: "ghost"}, d)
	if rc == 0 {
		t.Fatalf("expected non-zero rc; stderr=%s", errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "not found") {
		t.Errorf("stderr = %q, want not found", errBuf.String())
	}
}

