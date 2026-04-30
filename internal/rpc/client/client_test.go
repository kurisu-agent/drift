package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/kurisu-agent/drift/internal/rpc/client"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/wire"
)

// fakeTransport records the last request and returns a fixed response body.
type fakeTransport struct {
	lastRequest []byte
	lastCircuit string
	response    []byte
	err         error
}

func (f *fakeTransport) handle(_ context.Context, circuit string, req []byte) ([]byte, error) {
	f.lastCircuit = circuit
	f.lastRequest = append([]byte(nil), req...)
	return f.response, f.err
}

func TestCall_success(t *testing.T) {
	ft := &fakeTransport{
		response: []byte(`{"jsonrpc":"2.0","result":{"ok":true},"id":1}` + "\n"),
	}
	c := &client.Client{Transport: ft.handle}

	var got struct {
		OK bool `json:"ok"`
	}
	if err := c.Call(t.Context(), "my-server", wire.MethodServerVersion, map[string]string{"key": "v"}, &got); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !got.OK {
		t.Errorf("result.ok = false, want true")
	}
	if ft.lastCircuit != "my-server" {
		t.Errorf("circuit = %q, want my-server", ft.lastCircuit)
	}
	// Verify request shape.
	var req wire.Request
	if err := json.Unmarshal(ft.lastRequest, &req); err != nil {
		t.Fatalf("decode sent request: %v", err)
	}
	if req.Method != wire.MethodServerVersion {
		t.Errorf("method = %q, want %q", req.Method, wire.MethodServerVersion)
	}
	var params map[string]string
	if err := json.Unmarshal(req.Params, &params); err != nil {
		t.Fatalf("decode params: %v", err)
	}
	if diff := cmp.Diff(map[string]string{"key": "v"}, params); diff != "" {
		t.Errorf("params mismatch (-want +got):\n%s", diff)
	}
}

func TestCall_rpcError(t *testing.T) {
	ft := &fakeTransport{
		response: []byte(`{"jsonrpc":"2.0","error":{"code":3,"message":"kart 'x' not found","data":{"type":"kart_not_found","kart":"x"}},"id":1}` + "\n"),
	}
	c := &client.Client{Transport: ft.handle}

	err := c.Call(t.Context(), "my-server", "kart.info", nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var re *rpcerr.Error
	if !errors.As(err, &re) {
		t.Fatalf("want *rpcerr.Error, got %T: %v", err, err)
	}
	if re.Code != rpcerr.CodeNotFound {
		t.Errorf("code = %d, want %d", re.Code, rpcerr.CodeNotFound)
	}
	if re.Type != rpcerr.TypeKartNotFound {
		t.Errorf("type = %q, want %q", re.Type, rpcerr.TypeKartNotFound)
	}
	if re.Data["kart"] != "x" {
		t.Errorf("data.kart = %v, want x", re.Data["kart"])
	}
}

func TestCall_transportError(t *testing.T) {
	ft := &fakeTransport{err: &client.TransportError{ExitCode: 255, Stderr: "ssh: host unreachable"}}
	c := &client.Client{Transport: ft.handle}

	err := c.Call(t.Context(), "my-server", "kart.info", nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var te *client.TransportError
	if !errors.As(err, &te) {
		t.Fatalf("want *TransportError, got %T: %v", err, err)
	}
	if te.ExitCode != 255 {
		t.Errorf("exit = %d, want 255", te.ExitCode)
	}
}

func TestCall_idMismatch(t *testing.T) {
	ft := &fakeTransport{
		response: []byte(`{"jsonrpc":"2.0","result":{},"id":99}` + "\n"),
	}
	c := &client.Client{Transport: ft.handle}

	err := c.Call(t.Context(), "x", "ping", nil, nil)
	var te *client.TransportError
	if !errors.As(err, &te) {
		t.Fatalf("want *TransportError, got %T: %v", err, err)
	}
}

func TestCall_malformedResponse(t *testing.T) {
	ft := &fakeTransport{response: []byte(`garbage`)}
	c := &client.Client{Transport: ft.handle}

	err := c.Call(t.Context(), "x", "ping", nil, nil)
	var te *client.TransportError
	if !errors.As(err, &te) {
		t.Fatalf("want *TransportError, got %T: %v", err, err)
	}
}

func TestCall_nilParamsProducesEmptyObject(t *testing.T) {
	ft := &fakeTransport{
		response: []byte(`{"jsonrpc":"2.0","result":null,"id":1}` + "\n"),
	}
	c := &client.Client{Transport: ft.handle}

	if err := c.Call(t.Context(), "x", "ping", nil, nil); err != nil {
		t.Fatalf("Call: %v", err)
	}
	var req wire.Request
	if err := json.Unmarshal(ft.lastRequest, &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if string(req.Params) != `{}` {
		t.Errorf("params = %s, want {}", req.Params)
	}
}
