// Package wire defines the JSON-RPC 2.0 types. The protocol is one-shot:
// drift writes one [Request] to lakitu's stdin, reads one [Response] from
// stdout, SSH exits.
package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const Version = "2.0"

// Request: drift always sends integer id + named-param object.
// Notifications are not used.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	ID      json.RawMessage `json:"id"`
}

// Response: exactly one of Result or Error is set.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
	ID      json.RawMessage `json:"id"`
}

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

// DecodeRequest rejects missing jsonrpc/method/id, positional params, and
// non-"2.0" versions.
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

// EncodeResponse writes a single newline-terminated JSON object — stdout
// carries exactly one per lakitu rpc invocation.
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
