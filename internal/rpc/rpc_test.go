package rpc_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/wire"
)

func newRequest(t *testing.T, method, params string) *wire.Request {
	t.Helper()
	body := `{"jsonrpc":"2.0","method":"` + method + `","id":1`
	if params != "" {
		body += `,"params":` + params
	}
	body += `}`
	req, err := wire.DecodeRequest(strings.NewReader(body))
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	return req
}

func TestDispatch_methodNotFound(t *testing.T) {
	r := rpc.NewRegistry()
	resp := r.Dispatch(t.Context(), newRequest(t, "nope.missing", ""))
	if resp.Result != nil {
		t.Fatalf("unexpected result: %s", resp.Result)
	}
	if resp.Error == nil {
		t.Fatal("expected error")
	}
	if resp.Error.Code != int(rpcerr.CodeUserError) {
		t.Errorf("code = %d, want %d", resp.Error.Code, rpcerr.CodeUserError)
	}
	e := rpcerr.FromWire(resp.Error)
	if e.Type != "method_not_found" {
		t.Errorf("type = %q, want method_not_found", e.Type)
	}
	if e.Data["method"] != "nope.missing" {
		t.Errorf("data.method = %v, want nope.missing", e.Data["method"])
	}
}

func TestDispatch_success(t *testing.T) {
	r := rpc.NewRegistry()
	r.Register("ping", func(_ context.Context, _ json.RawMessage) (any, error) {
		return map[string]string{"pong": "ok"}, nil
	})

	resp := r.Dispatch(t.Context(), newRequest(t, "ping", ""))
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	var got map[string]string
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if diff := cmp.Diff(map[string]string{"pong": "ok"}, got); diff != "" {
		t.Errorf("result mismatch (-want +got):\n%s", diff)
	}
	if string(resp.ID) != "1" {
		t.Errorf("id = %q, want 1", resp.ID)
	}
}

func TestDispatch_rpcerrPreserved(t *testing.T) {
	r := rpc.NewRegistry()
	r.Register("bad", func(_ context.Context, _ json.RawMessage) (any, error) {
		return nil, rpcerr.NotFound(rpcerr.TypeKartNotFound, "kart %q not found", "x").With("kart", "x")
	})

	resp := r.Dispatch(t.Context(), newRequest(t, "bad", ""))
	if resp.Error == nil {
		t.Fatal("expected error")
	}
	if resp.Error.Code != int(rpcerr.CodeNotFound) {
		t.Errorf("code = %d, want %d", resp.Error.Code, rpcerr.CodeNotFound)
	}
	e := rpcerr.FromWire(resp.Error)
	if e.Type != rpcerr.TypeKartNotFound {
		t.Errorf("type = %q, want %q", e.Type, rpcerr.TypeKartNotFound)
	}
	if e.Data["kart"] != "x" {
		t.Errorf("data.kart = %v, want x", e.Data["kart"])
	}
}

func TestDispatch_plainErrorWrapped(t *testing.T) {
	r := rpc.NewRegistry()
	r.Register("boom", func(_ context.Context, _ json.RawMessage) (any, error) {
		return nil, errors.New("disk on fire")
	})

	resp := r.Dispatch(t.Context(), newRequest(t, "boom", ""))
	if resp.Error == nil {
		t.Fatal("expected error")
	}
	if resp.Error.Code != int(rpcerr.CodeInternal) {
		t.Errorf("code = %d, want %d", resp.Error.Code, rpcerr.CodeInternal)
	}
	if !strings.Contains(resp.Error.Message, "disk on fire") {
		t.Errorf("message = %q, want to contain disk on fire", resp.Error.Message)
	}
}

func TestDispatch_panicRecovered(t *testing.T) {
	r := rpc.NewRegistry()
	r.Register("panic", func(_ context.Context, _ json.RawMessage) (any, error) {
		panic("oops")
	})

	resp := r.Dispatch(t.Context(), newRequest(t, "panic", ""))
	if resp.Error == nil {
		t.Fatal("expected error")
	}
	if resp.Error.Code != int(rpcerr.CodeInternal) {
		t.Errorf("code = %d, want %d", resp.Error.Code, rpcerr.CodeInternal)
	}
	if !strings.Contains(resp.Error.Message, "oops") {
		t.Errorf("message = %q, want to contain oops", resp.Error.Message)
	}
}

func TestDispatch_resultMarshalFailure(t *testing.T) {
	r := rpc.NewRegistry()
	r.Register("bad-result", func(_ context.Context, _ json.RawMessage) (any, error) {
		return make(chan int), nil
	})

	resp := r.Dispatch(t.Context(), newRequest(t, "bad-result", ""))
	if resp.Error == nil {
		t.Fatal("expected error")
	}
	if resp.Error.Code != int(rpcerr.CodeInternal) {
		t.Errorf("code = %d, want %d", resp.Error.Code, rpcerr.CodeInternal)
	}
}

func TestBindParams_roundtrip(t *testing.T) {
	type P struct {
		Name string `json:"name"`
	}
	var p P
	if err := rpc.BindParams(json.RawMessage(`{"name":"x"}`), &p); err != nil {
		t.Fatalf("BindParams: %v", err)
	}
	if p.Name != "x" {
		t.Errorf("name = %q, want x", p.Name)
	}
}

func TestBindParams_unknownField(t *testing.T) {
	type P struct {
		Name string `json:"name"`
	}
	var p P
	err := rpc.BindParams(json.RawMessage(`{"name":"x","extra":1}`), &p)
	if err == nil {
		t.Fatal("expected error")
	}
	var re *rpcerr.Error
	if !errors.As(err, &re) {
		t.Fatalf("want *rpcerr.Error, got %T", err)
	}
	if re.Code != rpcerr.CodeUserError {
		t.Errorf("code = %d, want %d", re.Code, rpcerr.CodeUserError)
	}
}

func TestBindParams_empty(t *testing.T) {
	type P struct {
		Name string `json:"name"`
	}
	var p P
	if err := rpc.BindParams(nil, &p); err != nil {
		t.Fatalf("BindParams(nil): %v", err)
	}
	if p.Name != "" {
		t.Errorf("name = %q, want empty", p.Name)
	}
}

func TestRegister_duplicatePanics(t *testing.T) {
	r := rpc.NewRegistry()
	r.Register("x", func(context.Context, json.RawMessage) (any, error) { return nil, nil })
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on duplicate Register")
		}
	}()
	r.Register("x", func(context.Context, json.RawMessage) (any, error) { return nil, nil })
}
