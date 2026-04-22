package server_test

import (
	"encoding/json"
	"testing"

	"github.com/kurisu-agent/drift/internal/devpod"
	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/server"
	"github.com/kurisu-agent/drift/internal/wire"
)

// registerConnectAndDispatch mirrors the other two register*AndDispatch
// helpers — localizes the RPC surface to just kart.connect so a reader
// of the test can see what's registered without cross-referencing the
// production lakitu wire-up.
func registerConnectAndDispatch(t *testing.T, deps server.KartDeps, method string, params any) *wire.Response {
	t.Helper()
	reg := rpc.NewRegistry()
	server.RegisterKartConnect(reg, deps)
	// kart.connect depends on kart.session_env being registered too when
	// secrets are in play — register both so tests that need session env
	// can opt in.
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

// TestKartConnectReturnsEnvPrefixedArgv covers the happy path: the
// handler assembles the remote-command argv from the devpod Client's
// pinned binary path + configured DEVPOD_HOME. No session env in the
// kart config, so no --set-env suffix.
func TestKartConnectReturnsEnvPrefixedArgv(t *testing.T) {
	t.Parallel()
	deps := newKartDeps(t, &recordingDevpod{})
	// Override the default Binary/DevpodHome with explicit values so the
	// assertion below pins exact strings rather than whatever the fixture
	// happens to default to.
	deps.Devpod = &devpod.Client{
		Binary:     "/home/u/.drift/bin/devpod",
		DevpodHome: "/home/u/.drift/devpod",
	}
	writeKart(t, deps, "alpha", server.KartConfig{SourceMode: "clone", Repo: "u"})

	resp := registerConnectAndDispatch(t, deps, wire.MethodKartConnect,
		map[string]string{"name": "alpha"})
	if resp.Error != nil {
		t.Fatalf("dispatch error: %+v", resp.Error)
	}
	var got wire.KartConnectResult
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"env", "DEVPOD_HOME=/home/u/.drift/devpod",
		"/home/u/.drift/bin/devpod",
		"ssh", "alpha",
	}
	if len(got.Argv) != len(want) {
		t.Fatalf("argv = %v, want %v", got.Argv, want)
	}
	for i := range want {
		if got.Argv[i] != want[i] {
			t.Errorf("argv[%d] = %q, want %q", i, got.Argv[i], want[i])
		}
	}
}

// TestKartConnectMissingKartReturnsNotFound mirrors the kart.info /
// kart.start contract: connecting to a kart that doesn't exist returns
// a structured NotFound so the client can surface it the same way it
// surfaces every other missing-kart error.
func TestKartConnectMissingKartReturnsNotFound(t *testing.T) {
	t.Parallel()
	deps := newKartDeps(t, &recordingDevpod{})
	resp := registerConnectAndDispatch(t, deps, wire.MethodKartConnect,
		map[string]string{"name": "ghost"})
	if resp.Error == nil {
		t.Fatal("expected error for missing kart")
	}
	if resp.Error.Code != int(rpcerr.CodeNotFound) {
		t.Errorf("code = %d, want %d", resp.Error.Code, rpcerr.CodeNotFound)
	}
}

// TestKartConnectOmitsEnvPrefixWhenDevpodHomeUnset covers a defensive
// branch: if lakitu was bootstrapped without a DEVPOD_HOME (unusual, but
// possible in test fixtures), the argv must NOT start with a dangling
// `env` — skip the prefix entirely so the remote command is still
// valid.
func TestKartConnectOmitsEnvPrefixWhenDevpodHomeUnset(t *testing.T) {
	t.Parallel()
	deps := newKartDeps(t, &recordingDevpod{})
	deps.Devpod = &devpod.Client{Binary: "/bin/devpod"} // no DevpodHome
	writeKart(t, deps, "beta", server.KartConfig{SourceMode: "clone", Repo: "u"})

	resp := registerConnectAndDispatch(t, deps, wire.MethodKartConnect,
		map[string]string{"name": "beta"})
	if resp.Error != nil {
		t.Fatalf("dispatch error: %+v", resp.Error)
	}
	var got wire.KartConnectResult
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatal(err)
	}
	want := []string{"/bin/devpod", "ssh", "beta"}
	if len(got.Argv) != len(want) {
		t.Fatalf("argv = %v, want %v", got.Argv, want)
	}
	for i := range want {
		if got.Argv[i] != want[i] {
			t.Errorf("argv[%d] = %q, want %q", i, got.Argv[i], want[i])
		}
	}
}
