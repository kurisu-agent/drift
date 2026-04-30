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

	"github.com/kurisu-agent/drift/internal/wire"
)

// newTestDeps wires the CLI deps at a tempdir and returns the root of the
// fake $HOME so tests can assert on files written under ~/.config/drift.
//
// probeFn and probeInfoFn may each be nil when the test does not exercise
// that path.
func newTestDeps(t *testing.T,
	probeFn func(ctx context.Context, circuit string) (*probeResult, error),
	probeInfoFn func(ctx context.Context, sshArgs []string) (*wire.ServerInfo, error),
) (deps, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", "")
	cfgPath := filepath.Join(root, ".config", "drift", "config.yaml")
	return deps{
		clientConfigPath: func() (string, error) { return cfgPath, nil },
		probe:            probeFn,
		probeInfo:        probeInfoFn,
	}, root
}

func TestRunCircuitAdd_DiscoversNameFromServerInfo(t *testing.T) {
	called := false
	d, home := newTestDeps(t, nil, func(_ context.Context, sshArgs []string) (*wire.ServerInfo, error) {
		called = true
		if len(sshArgs) != 1 || sshArgs[0] != "dev@c1.example.com" {
			t.Errorf("sshArgs = %v, want [dev@c1.example.com]", sshArgs)
		}
		return &wire.ServerInfo{Name: "primary", Version: "1.2.3", API: 1}, nil
	})
	var out, errBuf bytes.Buffer
	io := IO{Stdout: &out, Stderr: &errBuf}

	cli := &CLI{}
	cmd := circuitAddCmd{UserHost: "dev@c1.example.com"}
	if rc := runCircuitAdd(context.Background(), io, cli, cmd, d); rc != 0 {
		t.Fatalf("runCircuitAdd rc=%d stderr=%s", rc, errBuf.String())
	}
	if !called {
		t.Fatal("probeInfo never invoked")
	}
	if !strings.Contains(out.String(), "registered circuit") || !strings.Contains(out.String(), "primary") {
		t.Errorf("stdout missing registration line: %q", out.String())
	}
	if !strings.Contains(out.String(), "lakitu 1.2.3") {
		t.Errorf("stdout missing lakitu version: %q", out.String())
	}
	// SSH block was written keyed by the server-reported name, not a
	// client-chosen slug.
	mf, err := os.ReadFile(filepath.Join(home, ".config", "drift", "ssh_config"))
	if err != nil {
		t.Fatalf("read managed: %v", err)
	}
	if !strings.Contains(string(mf), "Host drift.primary\n") {
		t.Errorf("expected Host drift.primary block, got:\n%s", mf)
	}
}

func TestRunCircuitAdd_ProbeFailureAborts(t *testing.T) {
	d, home := newTestDeps(t, nil, func(_ context.Context, _ []string) (*wire.ServerInfo, error) {
		return nil, errors.New("ssh: no route")
	})
	var out, errBuf bytes.Buffer
	io := IO{Stdout: &out, Stderr: &errBuf}

	rc := runCircuitAdd(context.Background(), io, &CLI{}, circuitAddCmd{UserHost: "dev@c1"}, d)
	if rc == 0 {
		t.Fatalf("expected non-zero rc; stderr=%s", errBuf.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "drift", "config.yaml")); !os.IsNotExist(err) {
		t.Errorf("client config should not be written when probe fails: err=%v", err)
	}
}

func TestRunCircuitAdd_InvalidServerNameIsInternalError(t *testing.T) {
	d, _ := newTestDeps(t, nil, func(_ context.Context, _ []string) (*wire.ServerInfo, error) {
		return &wire.ServerInfo{Name: "Has Spaces", Version: "x", API: 1}, nil
	})
	var out, errBuf bytes.Buffer
	io := IO{Stdout: &out, Stderr: &errBuf}
	rc := runCircuitAdd(context.Background(), io, &CLI{}, circuitAddCmd{UserHost: "dev@c1"}, d)
	if rc == 0 {
		t.Fatalf("expected non-zero rc")
	}
	if !strings.Contains(errBuf.String(), "invalid circuit name") {
		t.Errorf("stderr = %q, want invalid circuit name", errBuf.String())
	}
}

func TestRunCircuitAdd_NoSSHConfigSkipsSSHWrites(t *testing.T) {
	d, home := newTestDeps(t, nil, func(_ context.Context, _ []string) (*wire.ServerInfo, error) {
		return &wire.ServerInfo{Name: "c1", Version: "x", API: 1}, nil
	})
	var out, errBuf bytes.Buffer
	io := IO{Stdout: &out, Stderr: &errBuf}

	cmd := circuitAddCmd{UserHost: "dev@c1.example.com", NoSSHConfig: true}
	if rc := runCircuitAdd(context.Background(), io, &CLI{}, cmd, d); rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, errBuf.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "drift", "ssh_config")); !os.IsNotExist(err) {
		t.Errorf("expected managed ssh_config absent, got err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".ssh", "config")); !os.IsNotExist(err) {
		t.Errorf("expected user ssh config absent, got err=%v", err)
	}
}

func TestRunCircuitAdd_MissingUserReturnsUserError(t *testing.T) {
	d, _ := newTestDeps(t, nil, nil)
	var out, errBuf bytes.Buffer
	io := IO{Stdout: &out, Stderr: &errBuf}
	// Bare host with no "user@" prefix is rejected — lakitu needs an
	// explicit login user.
	rc := runCircuitAdd(context.Background(), io, &CLI{}, circuitAddCmd{UserHost: "bare.example.com"}, d)
	if rc == 0 {
		t.Fatalf("expected non-zero rc; stderr=%s", errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "user is required") {
		t.Errorf("stderr = %q, want 'user is required'", errBuf.String())
	}
}

func TestRunCircuitAdd_CollisionWithDifferentHost(t *testing.T) {
	d, _ := newTestDeps(t, nil, func(_ context.Context, _ []string) (*wire.ServerInfo, error) {
		return &wire.ServerInfo{Name: "c1", Version: "x", API: 1}, nil
	})
	var out, errBuf bytes.Buffer
	io := IO{Stdout: &out, Stderr: &errBuf}

	// First add registers c1 at host one.
	if rc := runCircuitAdd(context.Background(), io, &CLI{}, circuitAddCmd{UserHost: "dev@one"}, d); rc != 0 {
		t.Fatalf("first add rc=%d stderr=%s", rc, errBuf.String())
	}
	errBuf.Reset()

	// Second add with the same server-reported name but different host is
	// blocked with a name_collision so the user knows to rename one side.
	rc := runCircuitAdd(context.Background(), io, &CLI{}, circuitAddCmd{UserHost: "dev@two"}, d)
	if rc == 0 {
		t.Fatalf("expected collision error; stderr=%s", errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "name_collision") {
		t.Errorf("stderr = %q, want name_collision", errBuf.String())
	}
}

func TestRunCircuitList_JSONShape(t *testing.T) {
	d, _ := newTestDeps(t, nil, func(_ context.Context, _ []string) (*wire.ServerInfo, error) {
		return &wire.ServerInfo{Name: "c1", Version: "x", API: 1}, nil
	})
	var out, errBuf bytes.Buffer
	io := IO{Stdout: &out, Stderr: &errBuf}

	if rc := runCircuitAdd(context.Background(), io, &CLI{}, circuitAddCmd{
		UserHost: "dev@c1", NoSSHConfig: true,
	}, d); rc != 0 {
		t.Fatalf("seed rc=%d stderr=%s", rc, errBuf.String())
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
	nextInfo := "a"
	d, home := newTestDeps(t, nil, func(_ context.Context, _ []string) (*wire.ServerInfo, error) {
		info := &wire.ServerInfo{Name: nextInfo, Version: "x", API: 1}
		return info, nil
	})
	var out, errBuf bytes.Buffer
	io := IO{Stdout: &out, Stderr: &errBuf}

	for _, n := range []string{"a", "b"} {
		nextInfo = n
		if rc := runCircuitAdd(context.Background(), io, &CLI{}, circuitAddCmd{
			UserHost: "dev@" + n + ".example.com",
		}, d); rc != 0 {
			t.Fatalf("add %s rc=%d stderr=%s", n, rc, errBuf.String())
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
	d, _ := newTestDeps(t, nil, nil)
	var out, errBuf bytes.Buffer
	io := IO{Stdout: &out, Stderr: &errBuf}
	rc := runCircuitAdd(context.Background(), io, &CLI{}, circuitAddCmd{UserHost: "@nouser"}, d)
	if rc == 0 {
		t.Fatalf("expected non-zero rc; stderr=%s", errBuf.String())
	}
}

func TestRunCircuitRm_UnknownCircuit(t *testing.T) {
	d, _ := newTestDeps(t, nil, nil)
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
