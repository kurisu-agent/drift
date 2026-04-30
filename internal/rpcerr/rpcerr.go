// Package rpcerr defines the typed error used across drift and lakitu.
// A single [Error] value serializes into both the JSON-RPC error object on
// the RPC path and the stderr/exit-code pair on the human CLI path.
package rpcerr

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/kurisu-agent/drift/internal/wire"
)

// DataKeyDevpodStderr is the Data key under which server wrap sites attach
// a captured devpod stderr tail; errfmt renders it as a dim indented block
// below the main error message.
const DataKeyDevpodStderr = "devpod_stderr"

// DataKeyDevpodStdout is the Data key for captured devpod stdout. Devpod
// relays its tunnelserver progress AND most in-container failure messages
// to stdout (not stderr), so a stderr-only capture often comes back empty
// for real failures. Wrap sites attach both; errfmt renders this as a
// second dim block below the stderr one.
const DataKeyDevpodStdout = "devpod_stdout"

// Code doubles as process exit code on the human CLI path and populates
// JSON-RPC error.code on the RPC path.
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

// Type: prefer this over Code for programmatic branching — the stable
// snake_case identifier clients care about.
type Type string

const (
	TypeInternalError      Type = "internal_error"
	TypeInvalidName        Type = "invalid_name"
	TypeInvalidFlag        Type = "invalid_flag"
	TypeMutuallyExclusive  Type = "mutually_exclusive_flags"
	TypeKartNotFound       Type = "kart_not_found"
	TypeCircuitNotFound    Type = "circuit_not_found"
	TypeCharacterNotFound  Type = "character_not_found"
	TypeChestEntryNotFound Type = "chest_entry_not_found"
	TypePatNotFound        Type = "pat_not_found"
	TypePatInUse           Type = "pat_in_use"
	TypeSeedNotFound       Type = "seed_not_found"
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

// Error embeds the JSON-RPC error shape plus structured Data. Cause is
// hidden from clients (never serialized) but surfaces via errors.Unwrap.
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

// Is matches on Type — matching on Code alone would conflate unrelated
// errors that happen to share the same category.
func (e *Error) Is(target error) bool {
	var other *Error
	if !errors.As(target, &other) {
		return false
	}
	return e.Type == other.Type
}

// Wire produces the JSON-RPC error object. Cause is dropped — it's for
// internal logging only.
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

func New(code Code, typ Type, format string, args ...any) *Error {
	return &Error{Code: code, Type: typ, Message: fmt.Sprintf(format, args...)}
}

func (e *Error) With(key string, value any) *Error {
	if e.Data == nil {
		e.Data = make(map[string]any)
	}
	e.Data[key] = value
	return e
}

func (e *Error) Wrap(err error) *Error {
	e.Cause = err
	return e
}

func UserError(typ Type, format string, args ...any) *Error {
	return New(CodeUserError, typ, format, args...)
}

func NotFound(typ Type, format string, args ...any) *Error {
	return New(CodeNotFound, typ, format, args...)
}

func Conflict(typ Type, format string, args ...any) *Error {
	return New(CodeConflict, typ, format, args...)
}

// Internal is code 1 — the drift-level equivalent of JSON-RPC's -32603.
func Internal(format string, args ...any) *Error {
	return New(CodeInternal, TypeInternalError, format, args...)
}
