package server_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/kurisu-agent/drift/internal/devpod"
	"github.com/kurisu-agent/drift/internal/docker"
	driftexec "github.com/kurisu-agent/drift/internal/exec"
	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/server"
	"github.com/kurisu-agent/drift/internal/wire"
)

type singleReplyRunner struct {
	calls []driftexec.Cmd
	reply driftexec.Result
	err   error
}

func (r *singleReplyRunner) Run(_ context.Context, cmd driftexec.Cmd) (driftexec.Result, error) {
	r.calls = append(r.calls, cmd)
	return r.reply, r.err
}

func TestKartListUsesDockerBatchWhenWired(t *testing.T) {
	t.Parallel()
	// devpod list returns two workspaces with their UIDs. docker ps
	// returns a state for one of them; the other has no container so
	// kart.list must surface it as stopped without a per-workspace
	// `devpod status` shell.
	devpodRunner := &singleReplyRunner{reply: driftexec.Result{Stdout: []byte(`[
		{"id":"alpha","uid":"default-aa-001","source":{"gitRepository":"u-a"},"provider":{"name":"docker"}},
		{"id":"beta","uid":"default-bb-002","source":{"gitRepository":"u-b"},"provider":{"name":"docker"}}
	]`)}}
	dockerRunner := &singleReplyRunner{reply: driftexec.Result{Stdout: []byte(
		"default-aa-001 running\n",
	)}}
	deps := server.KartDeps{
		Devpod:    &devpod.Client{Binary: "devpod", Runner: devpodRunner},
		Docker:    &docker.Client{Binary: "docker", Runner: dockerRunner},
		GarageDir: t.TempDir(),
	}

	resp := registerAndDispatchWith(t, deps, wire.MethodKartList, struct{}{})
	if resp.Error != nil {
		t.Fatalf("dispatch: %+v", resp.Error)
	}
	var got server.KartListResult
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Karts) != 2 {
		t.Fatalf("got %d karts, want 2", len(got.Karts))
	}
	if got.Karts[0].Status != devpod.StatusRunning {
		t.Errorf("alpha status = %q, want running", got.Karts[0].Status)
	}
	if got.Karts[1].Status != devpod.StatusStopped {
		t.Errorf("beta status = %q, want stopped (no container)", got.Karts[1].Status)
	}

	// Verify no devpod status shells fired — the whole point of the
	// docker batch is to short-circuit them. Only `devpod list` should
	// have hit the devpod runner.
	if len(devpodRunner.calls) != 1 || devpodRunner.calls[0].Args[0] != "list" {
		t.Errorf("expected single 'devpod list' call, got %+v", devpodRunner.calls)
	}
	// docker ps fired exactly once with the expected filter.
	if len(dockerRunner.calls) != 1 {
		t.Fatalf("expected 1 docker call, got %d", len(dockerRunner.calls))
	}
	wantDockerArgs := []string{
		"ps", "-a",
		"--filter", "label=dev.containers.id",
		"--format", `{{.Label "dev.containers.id"}} {{.State}}`,
	}
	if diff := cmp.Diff(wantDockerArgs, dockerRunner.calls[0].Args); diff != "" {
		t.Errorf("docker args (-want +got):\n%s", diff)
	}
}

func TestServerStatusReturnsVersionAndKartsInOneCall(t *testing.T) {
	t.Parallel()
	devpodRunner := &singleReplyRunner{reply: driftexec.Result{Stdout: []byte(`[
		{"id":"alpha","uid":"default-aa-001","source":{"gitRepository":"u"},"provider":{"name":"docker"}}
	]`)}}
	dockerRunner := &singleReplyRunner{reply: driftexec.Result{Stdout: []byte(
		"default-aa-001 running\n",
	)}}
	deps := server.KartDeps{
		Devpod:    &devpod.Client{Binary: "devpod", Runner: devpodRunner},
		Docker:    &docker.Client{Binary: "docker", Runner: dockerRunner},
		GarageDir: t.TempDir(),
	}

	reg := rpc.NewRegistry()
	server.RegisterServerStatus(reg, deps)
	raw, err := json.Marshal(struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	resp := reg.Dispatch(t.Context(), &wire.Request{
		JSONRPC: wire.Version,
		Method:  wire.MethodServerStatus,
		Params:  raw,
		ID:      json.RawMessage("1"),
	})
	if resp.Error != nil {
		t.Fatalf("dispatch: %+v", resp.Error)
	}
	var got server.ServerStatusResult
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatal(err)
	}
	if got.Version == "" {
		t.Errorf("version empty: %+v", got)
	}
	if got.API < 1 {
		t.Errorf("api = %d, want >= 1", got.API)
	}
	if len(got.Karts) != 1 || got.Karts[0].Name != "alpha" || got.Karts[0].Status != devpod.StatusRunning {
		t.Errorf("karts = %+v, want one running alpha", got.Karts)
	}
}

// registerAndDispatchWith mirrors the kart_test helper but lets new
// status tests use a custom KartDeps without re-introducing the
// fakeDevpod-only newKartDeps wiring.
func registerAndDispatchWith(t *testing.T, deps server.KartDeps, method string, params any) *wire.Response {
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
