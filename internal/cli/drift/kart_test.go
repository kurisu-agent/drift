package drift

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/wire"
)

// newKartDeps wires drift CLI deps with a stub call hook and a default
// circuit preloaded in the config, so runKart* can dispatch without hitting
// real SSH.
func newKartDeps(t *testing.T, call func(ctx context.Context, circuit, method string, params, out any) error) (deps, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", "")
	cfgDir := filepath.Join(root, ".config", "drift")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(cfgDir, "config.yaml")
	yaml := "default_circuit: main\ncircuits:\n  main:\n    host: dev@main.example.com\n"
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	return deps{
		clientConfigPath: func() (string, error) { return cfgPath, nil },
		call:             call,
	}, root
}

func TestRunKartStart_SuccessRendersTextSummary(t *testing.T) {
	var gotMethod string
	var gotCircuit string
	d, _ := newKartDeps(t, func(_ context.Context, circuit, method string, params, out any) error {
		gotCircuit = circuit
		gotMethod = method
		// Echo a plausible KartLifecycleResult back to the caller.
		raw := json.RawMessage(`{"name":"alpha","status":"running"}`)
		*(out.(*json.RawMessage)) = raw
		return nil
	})
	var stdout, stderr bytes.Buffer
	io := IO{Stdout: &stdout, Stderr: &stderr}
	cli := &CLI{Output: "text"}

	rc := runKartStart(context.Background(), io, cli, startCmd{Name: "alpha"}, d)
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, stderr.String())
	}
	if gotCircuit != "main" {
		t.Errorf("circuit = %q, want main", gotCircuit)
	}
	if gotMethod != wire.MethodKartStart {
		t.Errorf("method = %q, want %q", gotMethod, wire.MethodKartStart)
	}
	if !strings.Contains(stdout.String(), `started kart "alpha" (status running)`) {
		t.Errorf("stdout = %q", stdout.String())
	}
}

func TestRunKartDelete_NotFoundBubblesUp(t *testing.T) {
	d, _ := newKartDeps(t, func(_ context.Context, _, _ string, _, _ any) error {
		return rpcerr.NotFound(rpcerr.TypeKartNotFound, "kart %q not found", "ghost")
	})
	var stdout, stderr bytes.Buffer
	io := IO{Stdout: &stdout, Stderr: &stderr}
	cli := &CLI{}

	rc := runKartDelete(context.Background(), io, cli, deleteCmd{Name: "ghost"}, d)
	if rc == 0 {
		t.Fatal("expected nonzero rc")
	}
	if !strings.Contains(stderr.String(), "kart_not_found") {
		t.Errorf("stderr = %q, want kart_not_found", stderr.String())
	}
}

func TestRunKartLogs_WritesChunkRaw(t *testing.T) {
	d, _ := newKartDeps(t, func(_ context.Context, _, method string, _, out any) error {
		if method != wire.MethodKartLogs {
			t.Errorf("method = %q, want %q", method, wire.MethodKartLogs)
		}
		raw := json.RawMessage(`{"name":"alpha","chunk":"line 1\nline 2\n"}`)
		*(out.(*json.RawMessage)) = raw
		return nil
	})
	var stdout, stderr bytes.Buffer
	io := IO{Stdout: &stdout, Stderr: &stderr}
	cli := &CLI{}

	rc := runKartLogs(context.Background(), io, cli, logsCmd{Name: "alpha"}, d)
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, stderr.String())
	}
	if stdout.String() != "line 1\nline 2\n" {
		t.Errorf("stdout = %q", stdout.String())
	}
}

func TestRunKart_NoDefaultCircuitFailsFast(t *testing.T) {
	// Overwrite the default circuit so resolveCircuit returns its "no
	// circuit" error before the stub is called.
	calls := 0
	d, home := newKartDeps(t, func(_ context.Context, _, _ string, _, _ any) error {
		calls++
		return nil
	})
	// Stomp the config with no default.
	cfgPath := filepath.Join(home, ".config", "drift", "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("circuits: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	io := IO{Stdout: &stdout, Stderr: &stderr}
	cli := &CLI{}

	rc := runKartStart(context.Background(), io, cli, startCmd{Name: "alpha"}, d)
	if rc == 0 {
		t.Fatal("expected nonzero rc when no default circuit")
	}
	if calls != 0 {
		t.Errorf("call was invoked %d time(s), want 0", calls)
	}
	if !strings.Contains(stderr.String(), "no circuit specified") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestRunKartStop_ExplicitCircuitOverridesDefault(t *testing.T) {
	var gotCircuit string
	d, _ := newKartDeps(t, func(_ context.Context, circuit, _ string, _, out any) error {
		gotCircuit = circuit
		raw := json.RawMessage(`{"name":"alpha","status":"stopped"}`)
		*(out.(*json.RawMessage)) = raw
		return nil
	})
	var stdout, stderr bytes.Buffer
	io := IO{Stdout: &stdout, Stderr: &stderr}
	cli := &CLI{Circuit: "secondary"}

	rc := runKartStop(context.Background(), io, cli, stopCmd{Name: "alpha"}, d)
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, stderr.String())
	}
	if gotCircuit != "secondary" {
		t.Errorf("circuit = %q, want secondary", gotCircuit)
	}
}

