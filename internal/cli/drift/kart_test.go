package drift

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

	// Force:true skips the interactive confirmation so this test exercises
	// the RPC-error pass-through, not the prompt.
	rc := runKartDelete(context.Background(), io, cli, deleteCmd{Name: "ghost", Force: true}, d)
	if rc == 0 {
		t.Fatal("expected nonzero rc")
	}
	if !strings.Contains(stderr.String(), "kart_not_found") {
		t.Errorf("stderr = %q, want kart_not_found", stderr.String())
	}
}

func TestRunKartLogs_TextLinesWrappedAsInfoRecords(t *testing.T) {
	d, _ := newKartDeps(t, func(_ context.Context, _, method string, _, out any) error {
		if method != wire.MethodKartLogs {
			t.Errorf("method = %q, want %q", method, wire.MethodKartLogs)
		}
		raw := json.RawMessage(`{"name":"alpha","format":"text","lines":["line 1","line 2"]}`)
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
	got := stdout.String()
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("line count = %d, want 2; stdout=%q", len(lines), got)
	}
	for i, want := range []string{"line 1", "line 2"} {
		if !strings.HasSuffix(lines[i], " INFO  "+want) {
			t.Errorf("line[%d] = %q, want suffix %q", i, lines[i], " INFO  "+want)
		}
	}
}

func TestRunKartLogs_JSONLRendersStructured(t *testing.T) {
	d, _ := newKartDeps(t, func(_ context.Context, _, _ string, _, out any) error {
		raw := json.RawMessage(`{"name":"alpha","format":"jsonl","lines":[` +
			`"{\"time\":\"2026-04-18T09:05:07Z\",\"level\":\"WARN\",\"msg\":\"slow\",\"kart\":\"alpha\"}"` +
			`]}`)
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
	got := stdout.String()
	want := "09:05:07 WARN  slow\n  kart: alpha\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRunKartLogs_DebugFlagDropsLevelFloor(t *testing.T) {
	d, _ := newKartDeps(t, func(_ context.Context, _, _ string, _, out any) error {
		raw := json.RawMessage(`{"name":"alpha","format":"jsonl","lines":[` +
			`"{\"time\":\"2026-04-18T09:05:07Z\",\"level\":\"DEBUG\",\"msg\":\"chatty\"}"` +
			`]}`)
		*(out.(*json.RawMessage)) = raw
		return nil
	})
	var stdout, stderr bytes.Buffer
	io := IO{Stdout: &stdout, Stderr: &stderr}

	// Without --debug, a DEBUG record is filtered out.
	rc := runKartLogs(context.Background(), io, &CLI{}, logsCmd{Name: "alpha"}, d)
	if rc != 0 || stdout.Len() != 0 {
		t.Fatalf("without --debug: rc=%d stdout=%q", rc, stdout.String())
	}

	// With --debug, the same record renders.
	stdout.Reset()
	rc = runKartLogs(context.Background(), io, &CLI{Debug: true}, logsCmd{Name: "alpha"}, d)
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, stderr.String())
	}
	if !strings.Contains(stdout.String(), "DEBUG") {
		t.Errorf("debug record not rendered: %q", stdout.String())
	}
}

func TestRunKartLogs_JSONOutputPassesThrough(t *testing.T) {
	raw := `{"name":"alpha","format":"text","lines":["a","b"]}`
	d, _ := newKartDeps(t, func(_ context.Context, _, _ string, _, out any) error {
		*(out.(*json.RawMessage)) = json.RawMessage(raw)
		return nil
	})
	var stdout, stderr bytes.Buffer
	io := IO{Stdout: &stdout, Stderr: &stderr}
	cli := &CLI{Output: "json"}

	rc := runKartLogs(context.Background(), io, cli, logsCmd{Name: "alpha"}, d)
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != raw {
		t.Errorf("got %q, want %q", stdout.String(), raw)
	}
}

func TestRunKartLogs_SendsFilterParams(t *testing.T) {
	var gotParams logsParams
	d, _ := newKartDeps(t, func(_ context.Context, _, _ string, params, out any) error {
		// deps.call receives the same struct value the caller passed — we're
		// the stub so we can type-assert back.
		if p, ok := params.(logsParams); ok {
			gotParams = p
		}
		*(out.(*json.RawMessage)) = json.RawMessage(`{"name":"alpha","format":"text","lines":[]}`)
		return nil
	})
	var stdout, stderr bytes.Buffer
	io := IO{Stdout: &stdout, Stderr: &stderr}

	rc := runKartLogs(context.Background(), io, &CLI{}, logsCmd{
		Name:  "alpha",
		Tail:  50,
		Since: 10 * time.Minute,
		Level: "warn",
		Grep:  "started",
	}, d)
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, stderr.String())
	}
	if gotParams.Name != "alpha" || gotParams.Tail != 50 || gotParams.Since != 10*time.Minute ||
		gotParams.Level != "warn" || gotParams.Grep != "started" {
		t.Errorf("params = %+v", gotParams)
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
