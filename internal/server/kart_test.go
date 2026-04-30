package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/kurisu-agent/drift/internal/devpod"
	driftexec "github.com/kurisu-agent/drift/internal/exec"
	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/server"
	"github.com/kurisu-agent/drift/internal/wire"
	"gopkg.in/yaml.v3"
)

// fakeDevpod replays canned exec results keyed by the first argument,
// which is always the devpod subcommand (list/status/...).
type fakeDevpod struct {
	replies map[string]fakeReply
}

type fakeReply struct {
	stdout string
	err    error
}

func (f *fakeDevpod) Run(_ context.Context, cmd driftexec.Cmd) (driftexec.Result, error) {
	if len(cmd.Args) == 0 {
		return driftexec.Result{}, errors.New("fakeDevpod: no args")
	}
	key := cmd.Args[0]
	r, ok := f.replies[key]
	if !ok {
		return driftexec.Result{}, errors.New("fakeDevpod: no reply for " + key)
	}
	return driftexec.Result{Stdout: []byte(r.stdout)}, r.err
}

func newKartDeps(t *testing.T, runner driftexec.Runner) server.KartDeps {
	t.Helper()
	garage := t.TempDir()
	if err := os.MkdirAll(filepath.Join(garage, "karts"), 0o755); err != nil {
		t.Fatal(err)
	}
	dp := &devpod.Client{Binary: "devpod", Runner: runner}
	// Skip the implicit `devpod context set-options` spawn so test
	// fakes (recordingDevpod, fakeDevpod) don't have to register a
	// reply for it. Production Clients still fire it on the first
	// Up — see TestEnsureContextOptionsFiresOnceBeforeFirstUp.
	devpod.MarkContextOptionsAppliedForTest(dp)
	return server.KartDeps{
		Devpod:    dp,
		GarageDir: garage,
	}
}

func writeKart(t *testing.T, deps server.KartDeps, name string, cfg server.KartConfig) {
	t.Helper()
	dir := filepath.Join(deps.GarageDir, "karts", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := yamlMarshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeAutostart(t *testing.T, deps server.KartDeps, name string) {
	t.Helper()
	path := filepath.Join(deps.GarageDir, "karts", name, "autostart")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

// registerAndDispatch routes a single request through a fresh registry so
// tests exercise the same dispatch path production uses — Bind, handler
// call, error-to-wire conversion.
func registerAndDispatch(t *testing.T, deps server.KartDeps, method string, params any) *wire.Response {
	t.Helper()
	reg := rpc.NewRegistry()
	server.RegisterKart(reg, deps)
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

func TestKartListMergesDevpodAndGarage(t *testing.T) {
	t.Parallel()
	fake := &fakeDevpod{
		replies: map[string]fakeReply{
			"list": {stdout: `[
				{"id":"alpha","source":{"gitRepository":"https://github.com/a/a.git"},"provider":{"name":"docker"}},
				{"id":"beta","source":{"gitRepository":"https://github.com/b/b.git"},"provider":{"name":"docker"}}
			]`},
			"status": {stdout: `{"state":"Running"}`},
		},
	}
	deps := newKartDeps(t, fake)
	writeKart(t, deps, "alpha", server.KartConfig{
		Repo: "https://github.com/a/a.git", Tune: "node", Character: "kurisu",
		SourceMode: "clone", User: "vscode", Shell: "/bin/zsh",
		Workdir: "/workspaces/alpha", Image: "base:ubuntu",
		CreatedAt: "2026-04-17T12:34:56Z",
	})
	writeAutostart(t, deps, "alpha")
	// beta has no garage entry — should still appear via devpod.

	resp := registerAndDispatch(t, deps, wire.MethodKartList, struct{}{})
	if resp.Error != nil {
		t.Fatalf("dispatch returned error: %+v", resp.Error)
	}

	var got server.KartListResult
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(got.Karts) != 2 {
		t.Fatalf("want 2 karts, got %d: %+v", len(got.Karts), got.Karts)
	}
	// Sorted order by name: alpha, beta.
	if got.Karts[0].Name != "alpha" || got.Karts[1].Name != "beta" {
		t.Errorf("order = %q,%q, want alpha,beta", got.Karts[0].Name, got.Karts[1].Name)
	}
	alpha := got.Karts[0]
	if alpha.Status != devpod.StatusRunning {
		t.Errorf("alpha.status = %q, want running", alpha.Status)
	}
	if alpha.Tune != "node" || alpha.Character != "kurisu" {
		t.Errorf("alpha metadata dropped: %+v", alpha)
	}
	if !alpha.Autostart {
		t.Errorf("alpha.autostart = false, want true")
	}
	if alpha.Source.Mode != "clone" || alpha.Source.URL != "https://github.com/a/a.git" {
		t.Errorf("alpha.source = %+v", alpha.Source)
	}
	if alpha.Container == nil || alpha.Container.Image != "base:ubuntu" {
		t.Errorf("alpha.container = %+v", alpha.Container)
	}
	if alpha.Devpod == nil || alpha.Devpod.Provider != "docker" {
		t.Errorf("alpha.devpod = %+v", alpha.Devpod)
	}
}

func TestKartListStaleEntryFromGarageOnly(t *testing.T) {
	t.Parallel()
	fake := &fakeDevpod{
		replies: map[string]fakeReply{
			"list": {stdout: `[]`},
		},
	}
	deps := newKartDeps(t, fake)
	writeKart(t, deps, "orphan", server.KartConfig{
		Repo: "https://example.com/orphan.git", SourceMode: "clone",
	})

	resp := registerAndDispatch(t, deps, wire.MethodKartList, struct{}{})
	if resp.Error != nil {
		t.Fatalf("dispatch returned error: %+v", resp.Error)
	}
	var got server.KartListResult
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Karts) != 1 {
		t.Fatalf("want 1 kart, got %d", len(got.Karts))
	}
	k := got.Karts[0]
	if k.Status != devpod.StatusError {
		t.Errorf("status = %q, want error (stale)", k.Status)
	}
	if !k.Stale {
		t.Errorf("stale = false, want true")
	}
	if k.Devpod != nil {
		t.Errorf("stale kart should have no devpod info, got %+v", k.Devpod)
	}
}

func TestKartListEmptyWhenNoKarts(t *testing.T) {
	t.Parallel()
	fake := &fakeDevpod{replies: map[string]fakeReply{"list": {stdout: `null`}}}
	deps := newKartDeps(t, fake)

	resp := registerAndDispatch(t, deps, wire.MethodKartList, struct{}{})
	if resp.Error != nil {
		t.Fatalf("dispatch returned error: %+v", resp.Error)
	}
	var got server.KartListResult
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatal(err)
	}
	// Must be a non-nil empty array so json.Marshal emits `"karts":[]`.
	if got.Karts == nil {
		t.Fatal("karts is nil")
	}
	if len(got.Karts) != 0 {
		t.Errorf("got %d karts, want 0", len(got.Karts))
	}
	if !bytesContainsString(resp.Result, `"karts":[]`) {
		t.Errorf("result = %s; want karts:[]", resp.Result)
	}
}

func TestKartListDevpodFailureSurfacesAsDevpodCode(t *testing.T) {
	t.Parallel()
	fake := &fakeDevpod{
		replies: map[string]fakeReply{
			"list": {err: &driftexec.Error{Name: "devpod", ExitCode: 1, FirstStderrLine: "cannot talk to docker"}},
		},
	}
	deps := newKartDeps(t, fake)

	resp := registerAndDispatch(t, deps, wire.MethodKartList, struct{}{})
	if resp.Error == nil {
		t.Fatal("expected error response")
	}
	if resp.Error.Code != int(rpcerr.CodeDevpod) {
		t.Errorf("code = %d, want %d", resp.Error.Code, rpcerr.CodeDevpod)
	}
	e := rpcerr.FromWire(resp.Error)
	if e.Type != rpcerr.TypeDevpodUnreachable {
		t.Errorf("type = %q, want devpod_unreachable", e.Type)
	}
}

func TestKartInfoRunningKart(t *testing.T) {
	t.Parallel()
	fake := &fakeDevpod{
		replies: map[string]fakeReply{
			"list":   {stdout: `[{"id":"myproject","source":{"gitRepository":"https://github.com/u/p.git"},"provider":{"name":"docker"}}]`},
			"status": {stdout: `{"state":"Running"}`},
		},
	}
	deps := newKartDeps(t, fake)
	writeKart(t, deps, "myproject", server.KartConfig{
		Repo: "https://github.com/u/p.git", SourceMode: "clone",
		Tune: "node", Character: "kurisu",
		User: "vscode", Shell: "/bin/zsh",
		Workdir: "/workspaces/myproject", Image: "base:ubuntu",
		CreatedAt: "2026-04-17T12:34:56Z",
	})

	resp := registerAndDispatch(t, deps, wire.MethodKartInfo, map[string]string{"name": "myproject"})
	if resp.Error != nil {
		t.Fatalf("dispatch error: %+v", resp.Error)
	}
	var got server.KartInfo
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatal(err)
	}
	want := server.KartInfo{
		Name:      "myproject",
		Status:    devpod.StatusRunning,
		CreatedAt: "2026-04-17T12:34:56Z",
		Source:    server.KartSource{Mode: "clone", URL: "https://github.com/u/p.git"},
		Tune:      "node",
		Character: "kurisu",
		Autostart: false,
		Container: &server.KartContainer{
			User: "vscode", Shell: "/bin/zsh",
			Workdir: "/workspaces/myproject", Image: "base:ubuntu",
		},
		Devpod: &server.KartDevpod{WorkspaceID: "myproject", Provider: "docker"},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
}

func TestKartInfoNotFound(t *testing.T) {
	t.Parallel()
	fake := &fakeDevpod{replies: map[string]fakeReply{"list": {stdout: `[]`}}}
	deps := newKartDeps(t, fake)

	resp := registerAndDispatch(t, deps, wire.MethodKartInfo, map[string]string{"name": "ghost"})
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
	if v, _ := e.Data["kart"].(string); v != "ghost" {
		t.Errorf("data.kart = %q, want ghost", v)
	}
}

func TestKartInfoStaleKart(t *testing.T) {
	t.Parallel()
	fake := &fakeDevpod{replies: map[string]fakeReply{"list": {stdout: `[]`}}}
	deps := newKartDeps(t, fake)
	writeKart(t, deps, "zombie", server.KartConfig{SourceMode: "clone", Repo: "https://z.example/z.git"})

	resp := registerAndDispatch(t, deps, wire.MethodKartInfo, map[string]string{"name": "zombie"})
	if resp.Error == nil {
		t.Fatal("expected error")
	}
	if resp.Error.Code != int(rpcerr.CodeConflict) {
		t.Errorf("code = %d, want %d", resp.Error.Code, rpcerr.CodeConflict)
	}
	e := rpcerr.FromWire(resp.Error)
	if e.Type != rpcerr.TypeStaleKart {
		t.Errorf("type = %q, want stale_kart", e.Type)
	}
	suggestion, _ := e.Data["suggestion"].(string)
	if suggestion == "" {
		t.Errorf("expected suggestion, got %+v", e.Data)
	}
}

func TestKartInfoRequiresName(t *testing.T) {
	t.Parallel()
	deps := newKartDeps(t, &fakeDevpod{replies: map[string]fakeReply{"list": {stdout: "[]"}}})
	resp := registerAndDispatch(t, deps, wire.MethodKartInfo, map[string]string{"name": ""})
	if resp.Error == nil {
		t.Fatal("expected error")
	}
	if resp.Error.Code != int(rpcerr.CodeUserError) {
		t.Errorf("code = %d, want %d", resp.Error.Code, rpcerr.CodeUserError)
	}
}

func TestKartInfoStoppedKartOmitsContainer(t *testing.T) {
	t.Parallel()
	fake := &fakeDevpod{
		replies: map[string]fakeReply{
			"list":   {stdout: `[{"id":"idle","source":{"gitRepository":"u"},"provider":{"name":"docker"}}]`},
			"status": {stdout: `{"state":"Stopped"}`},
		},
	}
	deps := newKartDeps(t, fake)
	writeKart(t, deps, "idle", server.KartConfig{
		SourceMode: "clone", Repo: "u",
		User: "vscode", Image: "img",
	})

	resp := registerAndDispatch(t, deps, wire.MethodKartInfo, map[string]string{"name": "idle"})
	if resp.Error != nil {
		t.Fatalf("dispatch: %+v", resp.Error)
	}
	var got server.KartInfo
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != devpod.StatusStopped {
		t.Errorf("status = %q, want stopped", got.Status)
	}
	if got.Container != nil {
		t.Errorf("container should be absent when not running, got %+v", got.Container)
	}
}

func TestKartListHandlesMissingKartsDir(t *testing.T) {
	t.Parallel()
	fake := &fakeDevpod{replies: map[string]fakeReply{"list": {stdout: `[]`}}}
	// Explicitly do not create the karts/ subdir.
	garage := t.TempDir()
	deps := server.KartDeps{
		Devpod:    &devpod.Client{Binary: "devpod", Runner: fake},
		GarageDir: garage,
	}

	resp := registerAndDispatch(t, deps, wire.MethodKartList, struct{}{})
	if resp.Error != nil {
		t.Fatalf("dispatch: %+v", resp.Error)
	}
	var got server.KartListResult
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Karts) != 0 {
		t.Errorf("want 0 karts, got %d", len(got.Karts))
	}
}

// bytesContainsString avoids pulling in strings just for a single use.
func bytesContainsString(b []byte, s string) bool {
	for i := 0; i+len(s) <= len(b); i++ {
		if string(b[i:i+len(s)]) == s {
			return true
		}
	}
	return false
}

func yamlMarshal(v any) ([]byte, error) {
	return yaml.Marshal(v)
}
