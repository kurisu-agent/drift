package errfmt_test

import (
	"bytes"
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

func TestEmit_RPCErrorRendersHeaderTypeAndDataLines(t *testing.T) {
	re := rpcerr.NotFound(rpcerr.TypeKartNotFound, "kart %q not found", "ghost").
		With("kart", "ghost")

	var buf bytes.Buffer
	rc := errfmt.Emit(&buf, re)

	if rc != int(rpcerr.CodeNotFound) {
		t.Errorf("rc = %d, want %d", rc, rpcerr.CodeNotFound)
	}
	want := "error: kart \"ghost\" not found\n  type: kart_not_found\n  kart: ghost\n"
	if got := buf.String(); got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

func TestEmit_DataKeysAreSortedForStableOutput(t *testing.T) {
	re := rpcerr.Conflict(rpcerr.TypeStaleKart, "kart %q is stale", "x").
		With("suggestion", "drift delete x").
		With("kart", "x")

	var buf bytes.Buffer
	errfmt.Emit(&buf, re)

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	// Header, type line, then data keys in lexicographic order — "kart" before
	// "suggestion".
	want := []string{
		`error: kart "x" is stale`,
		`  type: stale_kart`,
		`  kart: x`,
		`  suggestion: drift delete x`,
	}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d: %q", len(lines), len(want), buf.String())
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line[%d] = %q, want %q", i, lines[i], want[i])
		}
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
	out := buf.String()
	// The typed error must survive errors.As and surface as an indented
	// type: line — proves we didn't fall through to the untyped text path.
	if !strings.Contains(out, "  type: name_collision\n") {
		t.Errorf("missing type line in output: %q", out)
	}
	if !strings.HasPrefix(out, `error: kart "x" already exists`+"\n") {
		t.Errorf("wrong header line in output: %q", out)
	}
}

func TestEmit_DevpodStderrRendersAsIndentedBlockAndIsNotAKeyLine(t *testing.T) {
	re := rpcerr.New(rpcerr.CodeDevpod, rpcerr.TypeDevpodUpFailed,
		"devpod up failed").
		With("kart", "alpha").
		With(rpcerr.DataKeyDevpodStderr,
			"\x1b[31mwarn\x1b[0m resolving dependencies\nfatal: auth required")

	var buf bytes.Buffer
	errfmt.Emit(&buf, re)

	got := buf.String()
	if strings.Contains(got, "devpod_stderr:") {
		t.Errorf("devpod_stderr leaked as a key line: %q", got)
	}
	for _, want := range []string{
		"error: devpod up failed\n",
		"  kart: alpha\n",
		"  devpod output:\n",
		"    warn resolving dependencies\n",
		"    fatal: auth required\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in output:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\x1b[31m") {
		t.Errorf("ANSI escape leaked into block: %q", got)
	}
}

func TestEmit_EachCodeCategoryRoundTrips(t *testing.T) {
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
