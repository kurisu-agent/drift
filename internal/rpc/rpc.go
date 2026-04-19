// Package rpc implements the JSON-RPC 2.0 method registry and dispatcher
// shared by `lakitu rpc` and lakitu's human subcommands. [Registry.Dispatch]
// is the single place Go errors become [wire.Error] — handlers return plain
// Go values and errors.
package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/wire"
)

// Handler returns a json-marshallable value on success. Returning a
// *rpcerr.Error preserves structured fields; any other error becomes
// CodeInternal at the dispatch boundary.
type Handler func(ctx context.Context, params json.RawMessage) (any, error)

type Registry struct {
	methods map[string]Handler
}

func NewRegistry() *Registry {
	return &Registry{methods: make(map[string]Handler)}
}

// Register panics on duplicate names — that's a programmer bug.
func (r *Registry) Register(name string, h Handler) {
	if _, ok := r.methods[name]; ok {
		panic(fmt.Sprintf("rpc: method %q already registered", name))
	}
	r.methods[name] = h
}

func (r *Registry) Has(name string) bool {
	_, ok := r.methods[name]
	return ok
}

// Dispatch never returns an error: every failure (unknown method, handler
// error, marshal failure, handler panic) surfaces in resp.Error. The
// response always echoes req.ID.
func (r *Registry) Dispatch(ctx context.Context, req *wire.Request) *wire.Response {
	resp := &wire.Response{JSONRPC: wire.Version, ID: req.ID}

	h, ok := r.methods[req.Method]
	if !ok {
		e := rpcerr.New(rpcerr.CodeUserError, "method_not_found",
			"method %q not implemented", req.Method).With("method", req.Method)
		resp.Error = e.Wire()
		return resp
	}

	result, err := call(ctx, h, req.Params)
	if err != nil {
		resp.Error = toWire(err)
		return resp
	}

	raw, err := json.Marshal(result)
	if err != nil {
		resp.Error = rpcerr.Internal("marshal result for %q: %v", req.Method, err).Wire()
		return resp
	}
	resp.Result = raw
	return resp
}

// call recovers handler panics so a buggy handler cannot escape the
// dispatcher and corrupt stdout.
func call(ctx context.Context, h Handler, params json.RawMessage) (result any, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = rpcerr.Internal("handler panic: %v", r)
		}
	}()
	return h(ctx, params)
}

func toWire(err error) *wire.Error {
	var re *rpcerr.Error
	if errors.As(err, &re) {
		return re.Wire()
	}
	return rpcerr.Internal("%v", err).Wire()
}

// BindParams decodes with DisallowUnknownFields so handlers fail fast on
// client/server schema drift.
func BindParams(raw json.RawMessage, dst any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return rpcerr.UserError(rpcerr.TypeInvalidFlag, "invalid params: %v", err)
	}
	return nil
}
