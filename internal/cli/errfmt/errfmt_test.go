package errfmt_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/rpcerr"
)

func TestEmit_NilErrorReturnsOK(t *testing.T) {
	var buf bytes.Buffer
	if rc := errfmt.Emit(&buf, nil); rc != 0 {
		t.Errorf("rc = %d, want 0", rc)
	}
	if buf.Len() != 0 {
		t.Errorf("wrote %q on nil error, want empty", buf.String())
	}
}

func TestEmit_RPCErrorFormatsTwoLinesAndReturnsCode(t *testing.T) {
	re := rpcerr.NotFound(rpcerr.TypeKartNotFound, "kart %q not found", "ghost").
		With("kart", "ghost")

	var buf bytes.Buffer
	rc := errfmt.Emit(&buf, re)

	if rc != int(rpcerr.CodeNotFound) {
		t.Errorf("rc = %d, want %d", rc, rpcerr.CodeNotFound)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %q", len(lines), buf.String())
	}
	want := `error: kart "ghost" not found`
	if lines[0] != want {
		t.Errorf("line 1 = %q, want %q", lines[0], want)
	}
	// Line 2 must be valid single-line JSON of the error object.
	var got map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &got); err != nil {
		t.Fatalf("line 2 is not valid JSON: %v (%q)", err, lines[1])
	}
	if got["code"].(float64) != 3 {
		t.Errorf("code = %v, want 3", got["code"])
	}
	if got["message"] != `kart "ghost" not found` {
		t.Errorf("message = %v", got["message"])
	}
	data := got["data"].(map[string]any)
	if data["type"] != "kart_not_found" || data["kart"] != "ghost" {
		t.Errorf("data = %v", data)
	}
	if strings.Contains(lines[1], "\n") {
		t.Errorf("line 2 must be single-line JSON: %q", lines[1])
	}
}

func TestEmit_UntypedErrorUsesCodeOne(t *testing.T) {
	var buf bytes.Buffer
	rc := errfmt.Emit(&buf, errors.New("boom"))
	if rc != int(rpcerr.CodeInternal) {
		t.Errorf("rc = %d, want %d", rc, rpcerr.CodeInternal)
	}
	if got := buf.String(); got != "error: boom\n" {
		t.Errorf("output = %q, want %q", got, "error: boom\n")
	}
}

func TestEmit_WrappedRPCErrorStillRenderedTyped(t *testing.T) {
	re := rpcerr.Conflict(rpcerr.TypeNameCollision, "kart %q already exists", "x")
	wrapped := fmt.Errorf("outer wrap: %w", re)

	var buf bytes.Buffer
	rc := errfmt.Emit(&buf, wrapped)

	if rc != int(rpcerr.CodeConflict) {
		t.Errorf("rc = %d, want %d", rc, rpcerr.CodeConflict)
	}
	// Both halves must be present — the JSON line proves the typed error
	// survived errors.As and wasn't rendered as opaque text.
	if !strings.Contains(buf.String(), `"code":4`) {
		t.Errorf("missing code:4 in output: %q", buf.String())
	}
	if !strings.Contains(buf.String(), `"type":"name_collision"`) {
		t.Errorf("missing type in output: %q", buf.String())
	}
}

func TestEmit_EachCodeCategoryRoundTrips(t *testing.T) {
	// One test case per PLAN.md § "code values" row so a future code
	// renumber trips a clear assertion rather than a distant golden test.
	cases := []struct {
		name string
		err  *rpcerr.Error
		code int
	}{
		{"user_error", rpcerr.UserError(rpcerr.TypeInvalidFlag, "bad"), 2},
		{"not_found", rpcerr.NotFound(rpcerr.TypeKartNotFound, "nope"), 3},
		{"conflict", rpcerr.Conflict(rpcerr.TypeNameCollision, "dup"), 4},
		{"devpod", rpcerr.New(rpcerr.CodeDevpod, rpcerr.TypeDevpodUpFailed, "x"), 5},
		{"auth", rpcerr.New(rpcerr.CodeAuth, rpcerr.TypeSystemdDenied, "nope"), 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if rc := errfmt.Emit(&buf, tc.err); rc != tc.code {
				t.Errorf("rc = %d, want %d", rc, tc.code)
			}
			if !strings.HasPrefix(buf.String(), "error: ") {
				t.Errorf("missing error: prefix in %q", buf.String())
			}
		})
	}
}
