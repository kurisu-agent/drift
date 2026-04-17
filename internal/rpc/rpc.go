// Package rpc implements the JSON-RPC 2.0 method registry and dispatcher
// shared by both the `lakitu rpc` stdio path and lakitu's human subcommands.
//
// A [Registry] holds a map of method name → [Handler]. [Registry.Dispatch]
// is the single place where a Go error is translated into a [wire.Error];
// handlers themselves return plain Go values and errors.
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

// Handler executes a single JSON-RPC method. Implementations should return a
// value ready for `json.Marshal` on success, or an error on failure. Returning
// a `*rpcerr.Error` preserves structured fields; any other error is converted
// to a generic internal error at the dispatch boundary.
type Handler func(ctx context.Context, params json.RawMessage) (any, error)

// Registry maps method names to their handlers. The zero value is not usable;
// construct with [NewRegistry].
type Registry struct {
	methods map[string]Handler
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{methods: make(map[string]Handler)}
}

// Register associates name with h. A second call with the same name panics —
// duplicate registrations are a programmer bug.
func (r *Registry) Register(name string, h Handler) {
	if _, ok := r.methods[name]; ok {
		panic(fmt.Sprintf("rpc: method %q already registered", name))
	}
	r.methods[name] = h
}

// Has reports whether name has a registered handler.
func (r *Registry) Has(name string) bool {
	_, ok := r.methods[name]
	return ok
}

// Dispatch looks up the handler for req.Method, runs it, and returns a fully
// populated response envelope. It never returns an error: every failure mode
// (unknown method, handler error, result-marshal failure, handler panic) is
// represented in the response's Error field. The returned response always
// echoes req.ID.
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

// call invokes h while recovering from handler panics so a buggy handler
// cannot escape the dispatcher and corrupt stdout.
func call(ctx context.Context, h Handler, params json.RawMessage) (result any, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = rpcerr.Internal("handler panic: %v", r)
		}
	}()
	return h(ctx, params)
}

// toWire converts an arbitrary error into a wire-level error object. Non-
// rpcerr errors are wrapped as CodeInternal so clients see a structured
// payload instead of a bare string.
func toWire(err error) *wire.Error {
	var re *rpcerr.Error
	if errors.As(err, &re) {
		return re.Wire()
	}
	return rpcerr.Internal("%v", err).Wire()
}

// BindParams decodes a JSON-RPC params blob into dst. Unknown fields are
// rejected so handlers fail fast on client/server schema drift, and the
// resulting error is already a user-facing rpcerr.Error.
func BindParams(raw json.RawMessage, dst any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		// No params supplied — leave dst as the zero value. Handlers that
		// require fields should validate after binding.
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return rpcerr.UserError(rpcerr.TypeInvalidFlag, "invalid params: %v", err)
	}
	return nil
}
