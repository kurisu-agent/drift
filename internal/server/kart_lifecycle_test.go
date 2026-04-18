package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kurisu-agent/drift/internal/devpod"
	driftexec "github.com/kurisu-agent/drift/internal/exec"
	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/server"
	"github.com/kurisu-agent/drift/internal/wire"
)

// recordingDevpod tracks the sequence of devpod subcommands invoked and
// replays canned replies keyed by the first argument. Callers can pre-seed
// per-call stdout or an error to cover idempotency / failure paths.
type recordingDevpod struct {
	mu      sync.Mutex
	calls   []string
	replies map[string]fakeReply
}

func (r *recordingDevpod) Run(_ context.Context, cmd driftexec.Cmd) (driftexec.Result, error) {
	if len(cmd.Args) == 0 {
		return driftexec.Result{}, errors.New("recordingDevpod: no args")
	}
	r.mu.Lock()
	r.calls = append(r.calls, cmd.Args[0])
	r.mu.Unlock()
	reply, ok := r.replies[cmd.Args[0]]
	if !ok {
		return driftexec.Result{}, errors.New("recordingDevpod: no reply for " + cmd.Args[0])
	}
	return driftexec.Result{Stdout: []byte(reply.stdout)}, reply.err
}

// registerLifecycleAndDispatch is the lifecycle-side mirror of
// registerAndDispatch — we want RegisterKartLifecycle, not RegisterKart.
func registerLifecycleAndDispatch(t *testing.T, deps server.KartDeps, method string, params any) *wire.Response {
	t.Helper()
	reg := rpc.NewRegistry()
	server.RegisterKartLifecycle(reg, deps)
	raw, err := json.Marshal(params)
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

func TestKartStartInvokesDevpodUp(t *testing.T) {
	t.Parallel()
	fake := &recordingDevpod{
		replies: map[string]fakeReply{
			"up":     {stdout: "ok\n"},
			"status": {stdout: `{"state":"Running"}`},
		},
	}
	deps := newKartDeps(t, fake)
	writeKart(t, deps, "alpha", server.KartConfig{SourceMode: "clone", Repo: "u"})

	resp := registerLifecycleAndDispatch(t, deps, wire.MethodKartStart, map[string]string{"name": "alpha"})
	if resp.Error != nil {
		t.Fatalf("dispatch error: %+v", resp.Error)
	}
	var got server.KartLifecycleResult
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "alpha" || got.Status != devpod.StatusRunning {
		t.Errorf("result = %+v, want alpha/running", got)
	}
	if len(fake.calls) == 0 || fake.calls[0] != "up" {
		t.Errorf("expected first devpod call to be `up`, got %v", fake.calls)
	}
}

func TestKartStartIdempotentWhenAlreadyRunning(t *testing.T) {
	t.Parallel()
	// devpod up on a running workspace is a no-op success; the wrapper
	// returns exit 0. The lifecycle handler must therefore succeed too.
	fake := &recordingDevpod{
		replies: map[string]fakeReply{
			"up":     {stdout: ""}, // empty output, no error → success
			"status": {stdout: `{"state":"Running"}`},
		},
	}
	deps := newKartDeps(t, fake)

	resp := registerLifecycleAndDispatch(t, deps, wire.MethodKartStart, map[string]string{"name": "alpha"})
	if resp.Error != nil {
		t.Fatalf("dispatch error: %+v", resp.Error)
	}
	var got server.KartLifecycleResult
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != devpod.StatusRunning {
		t.Errorf("status = %q, want running", got.Status)
	}
}

func TestKartStartSurfacesDevpodFailure(t *testing.T) {
	t.Parallel()
	fake := &recordingDevpod{
		replies: map[string]fakeReply{
			"up": {err: &driftexec.Error{Name: "devpod", ExitCode: 2, FirstStderrLine: "docker unreachable"}},
		},
	}
	deps := newKartDeps(t, fake)

	resp := registerLifecycleAndDispatch(t, deps, wire.MethodKartStart, map[string]string{"name": "alpha"})
	if resp.Error == nil {
		t.Fatal("expected error response")
	}
	if resp.Error.Code != int(rpcerr.CodeDevpod) {
		t.Errorf("code = %d, want %d", resp.Error.Code, rpcerr.CodeDevpod)
	}
	e := rpcerr.FromWire(resp.Error)
	if e.Type != rpcerr.TypeDevpodUpFailed {
		t.Errorf("type = %q, want devpod_up_failed", e.Type)
	}
}

func TestKartStopInvokesDevpodStop(t *testing.T) {
	t.Parallel()
	fake := &recordingDevpod{
		replies: map[string]fakeReply{
			"stop":   {stdout: ""},
			"status": {stdout: `{"state":"Stopped"}`},
		},
	}
	deps := newKartDeps(t, fake)

	resp := registerLifecycleAndDispatch(t, deps, wire.MethodKartStop, map[string]string{"name": "alpha"})
	if resp.Error != nil {
		t.Fatalf("dispatch error: %+v", resp.Error)
	}
	var got server.KartLifecycleResult
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != devpod.StatusStopped {
		t.Errorf("status = %q, want stopped", got.Status)
	}
}

func TestKartRestartStopsThenStarts(t *testing.T) {
	t.Parallel()
	fake := &recordingDevpod{
		replies: map[string]fakeReply{
			"stop":   {stdout: ""},
			"up":     {stdout: ""},
			"status": {stdout: `{"state":"Running"}`},
		},
	}
	deps := newKartDeps(t, fake)

	resp := registerLifecycleAndDispatch(t, deps, wire.MethodKartRestart, map[string]string{"name": "alpha"})
	if resp.Error != nil {
		t.Fatalf("dispatch error: %+v", resp.Error)
	}
	// Verify ordering — stop must precede up.
	var stopIdx, upIdx = -1, -1
	for i, c := range fake.calls {
		if c == "stop" && stopIdx < 0 {
			stopIdx = i
		}
		if c == "up" && upIdx < 0 {
			upIdx = i
		}
	}
	if stopIdx < 0 || upIdx < 0 || stopIdx > upIdx {
		t.Errorf("expected stop→up ordering, got %v", fake.calls)
	}
}

func TestKartDeleteRemovesBothSides(t *testing.T) {
	t.Parallel()
	fake := &recordingDevpod{
		replies: map[string]fakeReply{
			"list":   {stdout: `[{"id":"alpha","provider":{"name":"docker"}}]`},
			"delete": {stdout: ""},
		},
	}
	deps := newKartDeps(t, fake)
	writeKart(t, deps, "alpha", server.KartConfig{SourceMode: "clone", Repo: "u"})

	resp := registerLifecycleAndDispatch(t, deps, wire.MethodKartDelete, map[string]string{"name": "alpha"})
	if resp.Error != nil {
		t.Fatalf("dispatch error: %+v", resp.Error)
	}
	var got server.KartLifecycleResult
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != devpod.StatusNotFound {
		t.Errorf("status = %q, want not_found", got.Status)
	}
	// Garage dir should be gone.
	dir := filepath.Join(deps.GarageDir, "karts", "alpha")
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("garage dir still present: %v", err)
	}
	// Devpod delete must have been invoked.
	sawDelete := false
	for _, c := range fake.calls {
		if c == "delete" {
			sawDelete = true
		}
	}
	if !sawDelete {
		t.Errorf("expected devpod delete call, got %v", fake.calls)
	}
}

func TestKartDeleteNotFound(t *testing.T) {
	t.Parallel()
	fake := &recordingDevpod{replies: map[string]fakeReply{"list": {stdout: `[]`}}}
	deps := newKartDeps(t, fake)

	resp := registerLifecycleAndDispatch(t, deps, wire.MethodKartDelete, map[string]string{"name": "ghost"})
	if resp.Error == nil {
		t.Fatal("expected error")
	}
	if resp.Error.Code != int(rpcerr.CodeNotFound) {
		t.Errorf("code = %d, want %d", resp.Error.Code, rpcerr.CodeNotFound)
	}
	e := rpcerr.FromWire(resp.Error)
	if e.Type != rpcerr.TypeKartNotFound {
		t.Errorf("type = %q, want kart_not_found", e.Type)
	}
}

func TestKartDeleteStaleKartCleansGarageOnly(t *testing.T) {
	t.Parallel()
	// Garage-only kart: delete should skip devpod (no workspace to remove)
	// and still clear the directory. This is the recovery path from a
	// stale_kart error.
	fake := &recordingDevpod{replies: map[string]fakeReply{"list": {stdout: `[]`}}}
	deps := newKartDeps(t, fake)
	writeKart(t, deps, "orphan", server.KartConfig{SourceMode: "clone", Repo: "u"})

	resp := registerLifecycleAndDispatch(t, deps, wire.MethodKartDelete, map[string]string{"name": "orphan"})
	if resp.Error != nil {
		t.Fatalf("dispatch error: %+v", resp.Error)
	}
	dir := filepath.Join(deps.GarageDir, "karts", "orphan")
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("garage dir still present: %v", err)
	}
	for _, c := range fake.calls {
		if c == "delete" {
			t.Errorf("devpod delete should not have been invoked for stale kart, calls=%v", fake.calls)
		}
	}
}

func TestKartLogsReturnsTextLines(t *testing.T) {
	t.Parallel()
	fake := &recordingDevpod{
		replies: map[string]fakeReply{
			"list": {stdout: `[{"id":"alpha","provider":{"name":"docker"}}]`},
			"logs": {stdout: "line one\nline two\n"},
		},
	}
	deps := newKartDeps(t, fake)

	resp := registerLifecycleAndDispatch(t, deps, wire.MethodKartLogs, map[string]string{"name": "alpha"})
	if resp.Error != nil {
		t.Fatalf("dispatch error: %+v", resp.Error)
	}
	var got server.KartLogsResult
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "alpha" {
		t.Errorf("name = %q, want alpha", got.Name)
	}
	if got.Format != server.LogFormatText {
		t.Errorf("format = %q, want %q", got.Format, server.LogFormatText)
	}
	if len(got.Lines) != 2 || got.Lines[0] != "line one" || got.Lines[1] != "line two" {
		t.Errorf("lines = %v, want [line one line two]", got.Lines)
	}
}

func TestKartLogsDetectsJSONL(t *testing.T) {
	t.Parallel()
	stdout := `{"time":"2026-04-18T09:05:07Z","level":"INFO","msg":"ready","kart":"alpha"}` + "\n" +
		`{"time":"2026-04-18T09:05:08Z","level":"WARN","msg":"slow"}` + "\n"
	fake := &recordingDevpod{
		replies: map[string]fakeReply{
			"list": {stdout: `[{"id":"alpha","provider":{"name":"docker"}}]`},
			"logs": {stdout: stdout},
		},
	}
	deps := newKartDeps(t, fake)

	resp := registerLifecycleAndDispatch(t, deps, wire.MethodKartLogs, map[string]string{"name": "alpha"})
	if resp.Error != nil {
		t.Fatalf("dispatch error: %+v", resp.Error)
	}
	var got server.KartLogsResult
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatal(err)
	}
	if got.Format != server.LogFormatJSONL {
		t.Errorf("format = %q, want %q", got.Format, server.LogFormatJSONL)
	}
	if len(got.Lines) != 2 {
		t.Fatalf("lines count = %d, want 2; got=%v", len(got.Lines), got.Lines)
	}
}

func TestKartLogsJSONLFallsBackToTextIfAnyLineNotJSON(t *testing.T) {
	t.Parallel()
	stdout := `{"time":"2026-04-18T09:05:07Z","level":"INFO","msg":"ready"}` + "\n" +
		`plain text line` + "\n"
	fake := &recordingDevpod{
		replies: map[string]fakeReply{
			"list": {stdout: `[{"id":"alpha","provider":{"name":"docker"}}]`},
			"logs": {stdout: stdout},
		},
	}
	deps := newKartDeps(t, fake)

	resp := registerLifecycleAndDispatch(t, deps, wire.MethodKartLogs, map[string]string{"name": "alpha"})
	if resp.Error != nil {
		t.Fatalf("dispatch error: %+v", resp.Error)
	}
	var got server.KartLogsResult
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatal(err)
	}
	if got.Format != server.LogFormatText {
		t.Errorf("format = %q, want text fallback", got.Format)
	}
}

func TestKartLogsAppliesTail(t *testing.T) {
	t.Parallel()
	fake := &recordingDevpod{
		replies: map[string]fakeReply{
			"list": {stdout: `[{"id":"alpha","provider":{"name":"docker"}}]`},
			"logs": {stdout: "a\nb\nc\nd\ne\n"},
		},
	}
	deps := newKartDeps(t, fake)

	resp := registerLifecycleAndDispatch(t, deps, wire.MethodKartLogs,
		map[string]any{"name": "alpha", "tail": 2})
	if resp.Error != nil {
		t.Fatalf("dispatch error: %+v", resp.Error)
	}
	var got server.KartLogsResult
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Lines) != 2 || got.Lines[0] != "d" || got.Lines[1] != "e" {
		t.Errorf("tail = %v, want [d e]", got.Lines)
	}
}

func TestKartLogsGrepTextLines(t *testing.T) {
	t.Parallel()
	fake := &recordingDevpod{
		replies: map[string]fakeReply{
			"list": {stdout: `[{"id":"alpha","provider":{"name":"docker"}}]`},
			"logs": {stdout: "kart started\nidle\nkart stopped\n"},
		},
	}
	deps := newKartDeps(t, fake)

	resp := registerLifecycleAndDispatch(t, deps, wire.MethodKartLogs,
		map[string]any{"name": "alpha", "grep": "kart"})
	if resp.Error != nil {
		t.Fatalf("dispatch error: %+v", resp.Error)
	}
	var got server.KartLogsResult
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Lines) != 2 {
		t.Errorf("lines = %v, want 2 matches", got.Lines)
	}
}

func TestKartLogsFiltersJSONLByLevel(t *testing.T) {
	t.Parallel()
	stdout := `{"time":"2026-04-18T09:05:07Z","level":"DEBUG","msg":"a"}` + "\n" +
		`{"time":"2026-04-18T09:05:08Z","level":"INFO","msg":"b"}` + "\n" +
		`{"time":"2026-04-18T09:05:09Z","level":"WARN","msg":"c"}` + "\n"
	fake := &recordingDevpod{
		replies: map[string]fakeReply{
			"list": {stdout: `[{"id":"alpha","provider":{"name":"docker"}}]`},
			"logs": {stdout: stdout},
		},
	}
	deps := newKartDeps(t, fake)

	resp := registerLifecycleAndDispatch(t, deps, wire.MethodKartLogs,
		map[string]any{"name": "alpha", "level": "warn"})
	if resp.Error != nil {
		t.Fatalf("dispatch error: %+v", resp.Error)
	}
	var got server.KartLogsResult
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Lines) != 1 {
		t.Fatalf("lines = %v, want 1 (WARN only)", got.Lines)
	}
	if !strings.Contains(got.Lines[0], `"msg":"c"`) {
		t.Errorf("remaining line wrong: %q", got.Lines[0])
	}
}

func TestKartLogsNotFound(t *testing.T) {
	t.Parallel()
	fake := &recordingDevpod{replies: map[string]fakeReply{"list": {stdout: `[]`}}}
	deps := newKartDeps(t, fake)

	resp := registerLifecycleAndDispatch(t, deps, wire.MethodKartLogs, map[string]string{"name": "ghost"})
	if resp.Error == nil {
		t.Fatal("expected error")
	}
	if resp.Error.Code != int(rpcerr.CodeNotFound) {
		t.Errorf("code = %d, want %d", resp.Error.Code, rpcerr.CodeNotFound)
	}
}

func TestKartLifecycleRequiresName(t *testing.T) {
	t.Parallel()
	fake := &recordingDevpod{replies: map[string]fakeReply{"list": {stdout: "[]"}}}
	deps := newKartDeps(t, fake)

	for _, method := range []string{
		wire.MethodKartStart,
		wire.MethodKartStop,
		wire.MethodKartRestart,
		wire.MethodKartDelete,
		wire.MethodKartLogs,
	} {
		resp := registerLifecycleAndDispatch(t, deps, method, map[string]string{"name": ""})
		if resp.Error == nil {
			t.Errorf("%s: expected error on empty name", method)
			continue
		}
		if resp.Error.Code != int(rpcerr.CodeUserError) {
			t.Errorf("%s: code = %d, want %d", method, resp.Error.Code, rpcerr.CodeUserError)
		}
	}
}
