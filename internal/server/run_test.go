package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/server"
	"github.com/kurisu-agent/drift/internal/wire"
)

func setupDeps(t *testing.T, yaml string) *server.Deps {
	t.Helper()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "runs.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatalf("seed runs.yaml: %v", err)
	}
	return &server.Deps{DriftHome: home}
}

func TestRunList_sortedAndMetadataOnly(t *testing.T) {
	d := setupDeps(t, `
runs:
  uptime:
    description: "load"
    mode: output
    command: 'uptime'
  ai:
    description: "claude"
    mode: interactive
    command: 'exec claude'
`)
	res, err := d.RunListHandler(context.Background(), nil)
	if err != nil {
		t.Fatalf("RunListHandler: %v", err)
	}
	lr, ok := res.(wire.RunListResult)
	if !ok {
		t.Fatalf("result type = %T", res)
	}
	if len(lr.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(lr.Entries))
	}
	if lr.Entries[0].Name != "ai" || lr.Entries[1].Name != "uptime" {
		t.Errorf("not sorted: %+v", lr.Entries)
	}
	if lr.Entries[0].Mode != wire.RunModeInteractive {
		t.Errorf("ai mode = %q", lr.Entries[0].Mode)
	}
}

func TestRunResolve_rendersTemplate(t *testing.T) {
	d := setupDeps(t, `
runs:
  ping:
    mode: output
    command: 'ping -c 4 {{ .Arg 0 | shq }}'
`)
	raw, _ := json.Marshal(wire.RunResolveParams{Name: "ping", Args: []string{"1.1.1.1"}})
	res, err := d.RunResolveHandler(context.Background(), raw)
	if err != nil {
		t.Fatalf("RunResolveHandler: %v", err)
	}
	rr := res.(wire.RunResolveResult)
	if rr.Command != `ping -c 4 '1.1.1.1'` {
		t.Errorf("Command = %q", rr.Command)
	}
	if rr.Mode != wire.RunModeOutput {
		t.Errorf("Mode = %q", rr.Mode)
	}
}

func TestRunResolve_notFound(t *testing.T) {
	d := setupDeps(t, "runs: {}\n")
	raw, _ := json.Marshal(wire.RunResolveParams{Name: "nope"})
	_, err := d.RunResolveHandler(context.Background(), raw)
	var re *rpcerr.Error
	if !errors.As(err, &re) {
		t.Fatalf("err = %v, want *rpcerr.Error", err)
	}
	if re.Code != rpcerr.CodeNotFound {
		t.Errorf("code = %d, want %d", re.Code, rpcerr.CodeNotFound)
	}
}

func TestRunResolve_requiresName(t *testing.T) {
	d := setupDeps(t, "runs: {}\n")
	raw, _ := json.Marshal(wire.RunResolveParams{})
	_, err := d.RunResolveHandler(context.Background(), raw)
	if err == nil {
		t.Fatal("expected error for empty name")
	}
}

// TestRunList_passesArgs: the handler must surface each entry's declared
// arg spec so the client-side interactive picker has something to prompt
// for. Without this the prompt UX would degrade to bare name selection.
func TestRunList_passesArgs(t *testing.T) {
	d := setupDeps(t, `
runs:
  ping:
    mode: output
    args:
      - name: host
        prompt: "Host"
        type: input
        default: "1.1.1.1"
    command: 'ping {{ .Arg 0 | shq }}'
`)
	res, err := d.RunListHandler(context.Background(), nil)
	if err != nil {
		t.Fatalf("RunListHandler: %v", err)
	}
	lr := res.(wire.RunListResult)
	if len(lr.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(lr.Entries))
	}
	args := lr.Entries[0].Args
	if len(args) != 1 || args[0].Name != "host" || args[0].Default != "1.1.1.1" || args[0].Type != wire.RunArgTypeInput {
		t.Errorf("args = %+v", args)
	}
}
