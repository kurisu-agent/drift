// Package wire defines the JSON-RPC 2.0 types exchanged between drift and lakitu.
//
// The protocol is one-shot: drift writes a single [Request] to lakitu's stdin,
// reads a single [Response] from its stdout, and SSH exits. See PLAN.md
// § RPC protocol for the full contract.
package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Version is the only value the "jsonrpc" field may take.
const Version = "2.0"

// Request is a JSON-RPC 2.0 request. drift always sends requests with an
// integer id and a named-parameter object; notifications are not used.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	ID      json.RawMessage `json:"id"`
}

// Response is a JSON-RPC 2.0 response. Exactly one of Result or Error is set.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
	ID      json.RawMessage `json:"id"`
}

// Error is the JSON-RPC 2.0 error object. See PLAN.md § Error handling for
// the code/type/data contract.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("jsonrpc: code=%d %s", e.Code, e.Message)
}

// DecodeRequest reads one JSON value from r and returns it as a [Request].
// It rejects requests that omit jsonrpc/method/id, use positional params,
// or use a jsonrpc version other than "2.0".
func DecodeRequest(r io.Reader) (*Request, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	var req Request
	if err := dec.Decode(&req); err != nil {
		return nil, fmt.Errorf("wire: decode request: %w", err)
	}
	if err := validateRequest(&req); err != nil {
		return nil, err
	}
	return &req, nil
}

// DecodeResponse reads one JSON value from r and returns it as a [Response].
func DecodeResponse(r io.Reader) (*Response, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	var resp Response
	if err := dec.Decode(&resp); err != nil {
		return nil, fmt.Errorf("wire: decode response: %w", err)
	}
	if resp.JSONRPC != Version {
		return nil, fmt.Errorf("wire: invalid jsonrpc version %q", resp.JSONRPC)
	}
	if (resp.Result == nil) == (resp.Error == nil) {
		return nil, errors.New("wire: response must set exactly one of result or error")
	}
	return &resp, nil
}

// EncodeResponse writes resp to w as a single newline-terminated JSON object.
// stdout carries exactly one JSON object per lakitu rpc invocation.
func EncodeResponse(w io.Writer, resp *Response) error {
	if resp.JSONRPC == "" {
		resp.JSONRPC = Version
	}
	buf, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("wire: encode response: %w", err)
	}
	buf = append(buf, '\n')
	if _, err := w.Write(buf); err != nil {
		return fmt.Errorf("wire: write response: %w", err)
	}
	return nil
}

// EncodeRequest writes req to w as a single newline-terminated JSON object.
func EncodeRequest(w io.Writer, req *Request) error {
	if req.JSONRPC == "" {
		req.JSONRPC = Version
	}
	buf, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("wire: encode request: %w", err)
	}
	buf = append(buf, '\n')
	if _, err := w.Write(buf); err != nil {
		return fmt.Errorf("wire: write request: %w", err)
	}
	return nil
}

func validateRequest(req *Request) error {
	if req.JSONRPC != Version {
		return fmt.Errorf("wire: invalid jsonrpc version %q", req.JSONRPC)
	}
	if req.Method == "" {
		return errors.New("wire: method required")
	}
	if len(req.ID) == 0 {
		return errors.New("wire: id required (notifications not supported)")
	}
	if len(req.Params) > 0 {
		trimmed := bytes.TrimSpace(req.Params)
		if len(trimmed) > 0 && trimmed[0] != '{' {
			return errors.New("wire: params must be an object (named params only)")
		}
	}
	return nil
}
