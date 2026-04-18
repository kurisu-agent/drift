// Package rpcerr defines the typed error used across drift and lakitu.
//
// A single [Error] value serializes into both halves of the error-handling
// contract: the JSON-RPC 2.0 error object on the RPC path, and the
// stderr/exit-code pair on the human CLI path.
package rpcerr

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/kurisu-agent/drift/internal/wire"
)

// Code is the small stable set of top-level error codes. On the human CLI
// path it doubles as the process exit code; on the RPC path it populates the
// JSON-RPC "error.code" field.
type Code int

const (
	CodeOK        Code = 0
	CodeInternal  Code = 1
	CodeUserError Code = 2
	CodeNotFound  Code = 3
	CodeConflict  Code = 4
	CodeDevpod    Code = 5
	CodeAuth      Code = 6
)

// Type is a stable snake_case identifier for programmatic branching on the
// client side. Prefer Type over Code in client code paths.
type Type string

const (
	TypeInternalError      Type = "internal_error"
	TypeInvalidName        Type = "invalid_name"
	TypeInvalidFlag        Type = "invalid_flag"
	TypeMutuallyExclusive  Type = "mutually_exclusive_flags"
	TypeKartNotFound       Type = "kart_not_found"
	TypeCharacterNotFound  Type = "character_not_found"
	TypeChestEntryNotFound Type = "chest_entry_not_found"
	TypeNameCollision      Type = "name_collision"
	TypeStaleKart          Type = "stale_kart"
	TypeAlreadyEnabled     Type = "already_enabled"
	TypeDevpodUpFailed     Type = "devpod_up_failed"
	TypeDevpodSSHFailed    Type = "devpod_ssh_failed"
	TypeDevpodUnreachable  Type = "devpod_unreachable"
	TypeChestBackendDenied Type = "chest_backend_denied"
	TypeGarageWriteDenied  Type = "garage_write_denied"
	TypeSystemdDenied      Type = "systemd_denied"
)

// Error is the canonical drift/lakitu error. It embeds the JSON-RPC error
// shape plus a structured Data map. Wrap underlying Go errors with Cause;
// the wrap is hidden from clients (never serialized) but surfaces via
// errors.Unwrap for logging.
type Error struct {
	Code    Code
	Type    Type
	Message string
	Data    map[string]any
	Cause   error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Type, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Type, e.Message)
}

func (e *Error) Unwrap() error { return e.Cause }

// Is supports errors.Is by matching on Type — the stable identifier clients
// care about. Matching on Code alone would conflate unrelated errors.
func (e *Error) Is(target error) bool {
	var other *Error
	if !errors.As(target, &other) {
		return false
	}
	return e.Type == other.Type
}

// MarshalJSON produces the JSON-RPC 2.0 error object: code/message at the
// top level, Type and any Data fields merged under "data".
func (e *Error) MarshalJSON() ([]byte, error) {
	data := make(map[string]any, len(e.Data)+1)
	for k, v := range e.Data {
		data[k] = v
	}
	if e.Type != "" {
		data["type"] = string(e.Type)
	}
	payload := struct {
		Code    Code           `json:"code"`
		Message string         `json:"message"`
		Data    map[string]any `json:"data,omitempty"`
	}{
		Code:    e.Code,
		Message: e.Message,
	}
	if len(data) > 0 {
		payload.Data = data
	}
	return json.Marshal(payload)
}

// Wire converts e into the JSON-RPC 2.0 error object used on the wire. The
// Cause is intentionally dropped — it is for internal logging only.
func (e *Error) Wire() *wire.Error {
	we := &wire.Error{Code: int(e.Code), Message: e.Message}
	data := make(map[string]any, len(e.Data)+1)
	for k, v := range e.Data {
		data[k] = v
	}
	if e.Type != "" {
		data["type"] = string(e.Type)
	}
	if len(data) > 0 {
		raw, err := json.Marshal(data)
		if err == nil {
			we.Data = raw
		}
	}
	return we
}

// FromWire reconstructs an Error from a wire-level error object. The "type"
// field inside Data is lifted back to Error.Type.
func FromWire(we *wire.Error) *Error {
	if we == nil {
		return nil
	}
	e := &Error{Code: Code(we.Code), Message: we.Message}
	if len(we.Data) == 0 {
		return e
	}
	var data map[string]any
	if err := json.Unmarshal(we.Data, &data); err != nil {
		return e
	}
	if t, ok := data["type"].(string); ok {
		e.Type = Type(t)
		delete(data, "type")
	}
	if len(data) > 0 {
		e.Data = data
	}
	return e
}

// New builds an Error. Callers typically use a helper below instead.
func New(code Code, typ Type, format string, args ...any) *Error {
	return &Error{Code: code, Type: typ, Message: fmt.Sprintf(format, args...)}
}

// With attaches structured context to an Error, returning the same pointer
// so calls chain.
func (e *Error) With(key string, value any) *Error {
	if e.Data == nil {
		e.Data = make(map[string]any)
	}
	e.Data[key] = value
	return e
}

// Wrap attaches an underlying Go error as the Cause. The cause is never
// serialized to clients.
func (e *Error) Wrap(err error) *Error {
	e.Cause = err
	return e
}

// UserError is code 2 — any flag/argument mistake on the CLI.
func UserError(typ Type, format string, args ...any) *Error {
	return New(CodeUserError, typ, format, args...)
}

// NotFound is code 3 — the requested resource doesn't exist.
func NotFound(typ Type, format string, args ...any) *Error {
	return New(CodeNotFound, typ, format, args...)
}

// Conflict is code 4 — name collisions, stale state, already-in-state errors.
func Conflict(typ Type, format string, args ...any) *Error {
	return New(CodeConflict, typ, format, args...)
}

// Internal is code 1 — programmer error or unexpected failure surfaced to the
// client. The matching JSON-RPC code at the dispatch boundary is -32603; this
// is the drift-level exit-code equivalent.
func Internal(format string, args ...any) *Error {
	return New(CodeInternal, TypeInternalError, format, args...)
}
