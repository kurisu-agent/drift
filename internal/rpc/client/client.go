// Package client is the drift-side helper for issuing a JSON-RPC 2.0 call
// to a remote lakitu over SSH. Every non-local drift subcommand resolves
// to one [Client.Call] which shells `ssh drift.<circuit> lakitu rpc`.
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

// Transport sends a marshalled JSON-RPC request to the circuit and returns
// the raw response. Any failure that prevented delivery or intact receipt
// must surface as a *TransportError.
type Transport func(ctx context.Context, circuit string, request []byte) (response []byte, err error)

// TransportError covers failures before a JSON-RPC response arrives. drift
// preserves ssh's exit code and stderr verbatim so the user sees the real
// diagnostic rather than a fabricated envelope.
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

type Client struct {
	Transport Transport
	// nextID overrides the request id (tests). Nil uses `1` per call.
	nextID func() json.RawMessage
}

func New() *Client {
	return &Client{Transport: SSHTransport()}
}

// Call issues a single RPC. params=nil sends `{}`; result=nil discards the
// response payload. RPC-level errors always come back as *rpcerr.Error.
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
	// Fixed id 1 per call: each SSH invocation is a fresh process with a
	// single request/response pair, so there is nothing to collide with.
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

// SSHTransport shells `ssh drift.<circuit> lakitu rpc` via driftexec.Run so
// it inherits the standard Cancel/WaitDelay discipline. The alias is a
// drift-managed Host entry in ~/.config/drift/ssh_config.
func SSHTransport() Transport {
	return func(ctx context.Context, circuit string, request []byte) ([]byte, error) {
		alias := "drift." + circuit
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
