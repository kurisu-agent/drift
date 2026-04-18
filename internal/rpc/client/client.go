// Package client is the drift-side helper for issuing a single JSON-RPC 2.0
// call to a remote lakitu over SSH.
//
// Every non-local drift subcommand resolves to one [Client.Call] which
// shells out to `ssh drift.<circuit> lakitu rpc`. A transport failure (SSH
// itself exited non-zero) surfaces as [*TransportError]; an RPC-level error
// arrives as [*rpcerr.Error] with its Code, Type, and Data preserved.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	driftexec "github.com/kurisu-agent/drift/internal/exec"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/wire"
)

// Transport sends a single marshalled JSON-RPC request to the given circuit
// and returns the raw response bytes. Implementations must return a
// [*TransportError] for any failure that prevented the request from being
// delivered or the response from being received intact.
type Transport func(ctx context.Context, circuit string, request []byte) (response []byte, err error)

// TransportError indicates that SSH (or whatever transport is in use) failed
// before a JSON-RPC response could be read. drift preserves the transport's
// own exit code and stderr so the user sees the real diagnostic ("ssh: Could
// not resolve hostname ...") rather than a fabricated envelope.
type TransportError struct {
	ExitCode int
	Stderr   string
	Cause    error
}

func (e *TransportError) Error() string {
	if e.Stderr != "" {
		return fmt.Sprintf("transport error (exit %d): %s", e.ExitCode, e.Stderr)
	}
	if e.Cause != nil {
		return fmt.Sprintf("transport error (exit %d): %v", e.ExitCode, e.Cause)
	}
	return fmt.Sprintf("transport error (exit %d)", e.ExitCode)
}

func (e *TransportError) Unwrap() error { return e.Cause }

// Client performs one JSON-RPC call per invocation.
//
// The zero value is not usable; call [New] (or set Transport manually).
type Client struct {
	Transport Transport

	// nextID returns the id encoded into each request. Overridable for tests;
	// when nil, a monotonically increasing integer starting at 1 is used.
	nextID func() json.RawMessage
}

// New returns a Client backed by the SSH transport.
func New() *Client {
	return &Client{Transport: SSHTransport()}
}

// Call issues a single RPC against circuit. params is marshalled as the JSON
// params object; pass nil to send `{}`. On success, result (which may be nil)
// is populated from the response's result. On an RPC-level error the returned
// error is always a *rpcerr.Error.
func (c *Client) Call(ctx context.Context, circuit, method string, params, result any) error {
	if c.Transport == nil {
		return rpcerr.Internal("rpc client: no transport configured")
	}

	id := c.allocID()
	reqBody, err := buildRequest(id, method, params)
	if err != nil {
		return rpcerr.Internal("build request: %v", err).Wrap(err)
	}

	respBody, err := c.Transport(ctx, circuit, reqBody)
	if err != nil {
		return err
	}

	resp, err := wire.DecodeResponse(bytes.NewReader(respBody))
	if err != nil {
		return &TransportError{Cause: fmt.Errorf("decode response: %w", err)}
	}
	if !bytes.Equal(resp.ID, id) {
		return &TransportError{Cause: fmt.Errorf("response id %s does not match request id %s", resp.ID, id)}
	}
	if resp.Error != nil {
		return rpcerr.FromWire(resp.Error)
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(resp.Result, result); err != nil {
		return rpcerr.Internal("decode result for %q: %v", method, err).Wrap(err)
	}
	return nil
}

func (c *Client) allocID() json.RawMessage {
	if c.nextID != nil {
		return c.nextID()
	}
	// Default: fixed id 1 per call. Each SSH invocation is a fresh process
	// with a single request/response pair, so there is nothing to collide.
	return json.RawMessage(`1`)
}

func buildRequest(id json.RawMessage, method string, params any) ([]byte, error) {
	raw := json.RawMessage(`{}`)
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	req := &wire.Request{
		JSONRPC: wire.Version,
		Method:  method,
		Params:  raw,
		ID:      id,
	}
	return json.Marshal(req)
}

// SSHTransport returns the default Transport that shells out to
// `ssh drift.<circuit> lakitu rpc`. Any non-zero exit from ssh is wrapped in
// a [*TransportError] carrying ssh's exit code and stderr verbatim.
//
// The ssh invocation routes through [driftexec.Run] so it inherits the
// Cancel/WaitDelay discipline from plans/PLAN.md § "Critical invariants".
func SSHTransport() Transport {
	return func(ctx context.Context, circuit string, request []byte) ([]byte, error) {
		alias := "drift." + circuit
		// SSH alias is a drift-managed Host entry from ~/.config/drift/ssh_config;
		// argv is built directly (no shell) so circuit interpolation is safe.
		res, err := driftexec.Run(ctx, driftexec.Cmd{
			Name:  "ssh",
			Args:  []string{alias, "lakitu", "rpc"},
			Stdin: bytes.NewReader(request),
		})
		if err != nil {
			te := &TransportError{ExitCode: -1, Cause: err}
			var ee *driftexec.Error
			if errors.As(err, &ee) {
				te.ExitCode = ee.ExitCode
				te.Stderr = string(ee.Stderr)
			}
			return nil, te
		}
		return res.Stdout, nil
	}
}
