package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	driftexec "github.com/kurisu-agent/drift/internal/exec"
	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/server"
	"github.com/kurisu-agent/drift/internal/systemd"
	"github.com/kurisu-agent/drift/internal/wire"
)

// stubSystemdRunner records every systemctl argv so the test can assert on
// invocation counts. Every call returns empty stdout + nil error, mirroring
// systemctl's own idempotent semantics for `enable --now` / `disable --now`
// on an already-enabled/disabled unit.
type stubSystemdRunner struct {
	mu    sync.Mutex
	calls [][]string
}

func (r *stubSystemdRunner) Run(_ context.Context, cmd driftexec.Cmd) (driftexec.Result, error) {
	r.mu.Lock()
	r.calls = append(r.calls, append([]string(nil), cmd.Args...))
	r.mu.Unlock()
	return driftexec.Result{}, nil
}

func newAutostartDeps(t *testing.T) (server.KartAutostartDeps, *stubSystemdRunner) {
	t.Helper()
	garage := t.TempDir()
	if err := os.MkdirAll(filepath.Join(garage, "karts", "alpha"), 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &stubSystemdRunner{}
	deps := server.KartAutostartDeps{
		GarageDir: garage,
		Systemd: &systemd.Client{
			Binary: "systemctl",
			Runner: runner,
		},
	}
	return deps, runner
}

func dispatchAutostart(t *testing.T, deps server.KartAutostartDeps, method, name string) *wire.Response {
	t.Helper()
	reg := rpc.NewRegistry()
	server.RegisterKartAutostart(reg, deps)
	raw, err := json.Marshal(map[string]string{"name": name})
	if err != nil {
		t.Fatal(err)
	}
	return reg.Dispatch(t.Context(), &wire.Request{
		JSONRPC: wire.Version,
		Method:  method,
		Params:  raw,
		ID:      json.RawMessage("1"),
	})
}

// TestKartEnableIsIdempotent pins the handler-level contract: calling
// kart.enable twice in a row succeeds both times, the marker file is present
// after each call, and systemctl is invoked once per call. systemctl --user
// enable --now is itself idempotent; this test guards against the handler
// gaining state (e.g. an "already-enabled" short-circuit) that would
// diverge from that contract without also covering it.
func TestKartEnableIsIdempotent(t *testing.T) {
	t.Parallel()
	deps, runner := newAutostartDeps(t)
	markerPath := filepath.Join(deps.GarageDir, "karts", "alpha", "autostart")

	for i := range 2 {
		resp := dispatchAutostart(t, deps, wire.MethodKartEnable, "alpha")
		if resp.Error != nil {
			t.Fatalf("call %d: dispatch error: %+v", i+1, resp.Error)
		}
		var got server.AutostartResult
		if err := json.Unmarshal(resp.Result, &got); err != nil {
			t.Fatalf("call %d: decode result: %v", i+1, err)
		}
		if got.Name != "alpha" || !got.Enabled {
			t.Errorf("call %d: result = %+v, want {alpha true}", i+1, got)
		}
		if _, err := os.Stat(markerPath); err != nil {
			t.Errorf("call %d: marker missing: %v", i+1, err)
		}
	}
	if len(runner.calls) != 2 {
		t.Errorf("systemctl invocations = %d, want 2 (one per enable call)", len(runner.calls))
	}
}

// TestKartDisableIsIdempotent mirrors the enable test for the
// disable path — two sequential disables both succeed and leave no marker.
// The first disable has a marker to remove; the second hits the ErrNotExist
// branch in removeAutostartMarker and still succeeds.
func TestKartDisableIsIdempotent(t *testing.T) {
	t.Parallel()
	deps, runner := newAutostartDeps(t)
	markerPath := filepath.Join(deps.GarageDir, "karts", "alpha", "autostart")
	if err := os.WriteFile(markerPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	for i := range 2 {
		resp := dispatchAutostart(t, deps, wire.MethodKartDisable, "alpha")
		if resp.Error != nil {
			t.Fatalf("call %d: dispatch error: %+v", i+1, resp.Error)
		}
		var got server.AutostartResult
		if err := json.Unmarshal(resp.Result, &got); err != nil {
			t.Fatalf("call %d: decode result: %v", i+1, err)
		}
		if got.Name != "alpha" || got.Enabled {
			t.Errorf("call %d: result = %+v, want {alpha false}", i+1, got)
		}
		if _, err := os.Stat(markerPath); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("call %d: marker should be absent, got err=%v", i+1, err)
		}
	}
	if len(runner.calls) != 2 {
		t.Errorf("systemctl invocations = %d, want 2 (one per disable call)", len(runner.calls))
	}
}
