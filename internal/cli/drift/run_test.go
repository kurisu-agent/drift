package drift

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/wire"
)

// TestRunRunExec_NoNameListsRuns covers the `drift run` (no positional)
// affordance: instead of Kong's "expected <name>", fall through to the
// same listing `drift runs` produces so users learn what's available.
func TestRunRunExec_NoNameListsRuns(t *testing.T) {
	gotMethods := []string{}
	d, _ := newKartDeps(t, func(_ context.Context, _, method string, _, out any) error {
		gotMethods = append(gotMethods, method)
		if method != wire.MethodRunList {
			t.Fatalf("unexpected method %q (want %q)", method, wire.MethodRunList)
		}
		// Echo a canned RunListResult so the renderer has something to print.
		res := out.(*wire.RunListResult)
		*res = wire.RunListResult{Entries: []wire.RunEntry{
			{Name: "ai", Description: "claude loop", Mode: wire.RunModeInteractive},
			{Name: "uptime", Description: "uptime", Mode: wire.RunModeOutput},
		}}
		return nil
	})
	var stdout, stderr bytes.Buffer
	io := IO{Stdout: &stdout, Stderr: &stderr}
	cli := &CLI{Output: "text"}

	rc := runRunExec(context.Background(), io, cli, runCmd{ /* Name intentionally empty */ }, d)
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, stderr.String())
	}
	if len(gotMethods) != 1 || gotMethods[0] != wire.MethodRunList {
		t.Errorf("methods called = %v, want [%s]", gotMethods, wire.MethodRunList)
	}
	if !strings.Contains(stdout.String(), "ai") || !strings.Contains(stdout.String(), "uptime") {
		t.Errorf("stdout missing run names:\n%s", stdout.String())
	}
}

// TestRunRunExec_NoNameEmptyListsShowsHint covers the empty-registry edge:
// the server returns zero entries, the client should still render the
// "no runs configured" pointer instead of panicking or printing a header
// with no rows.
func TestRunRunExec_NoNameEmptyListsShowsHint(t *testing.T) {
	d, _ := newKartDeps(t, func(_ context.Context, _, method string, _, out any) error {
		if method == wire.MethodRunList {
			*out.(*wire.RunListResult) = wire.RunListResult{}
			return nil
		}
		t.Fatalf("unexpected method %q", method)
		return nil
	})
	var stdout, stderr bytes.Buffer
	io := IO{Stdout: &stdout, Stderr: &stderr}

	rc := runRunExec(context.Background(), io, &CLI{Output: "text"}, runCmd{}, d)
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, stderr.String())
	}
	if !strings.Contains(stdout.String(), "no runs configured") {
		t.Errorf("expected 'no runs configured' hint, got:\n%s", stdout.String())
	}
}

// TestRunRunExec_JSONNoNameReturnsJSON covers the parity with `drift runs`
// under --output json: an empty positional falls through to list and
// serializes the RunListResult as JSON, not a table.
func TestRunRunExec_JSONNoNameReturnsJSON(t *testing.T) {
	d, _ := newKartDeps(t, func(_ context.Context, _, _ string, _, out any) error {
		*out.(*wire.RunListResult) = wire.RunListResult{Entries: []wire.RunEntry{
			{Name: "ai", Mode: wire.RunModeInteractive},
		}}
		return nil
	})
	var stdout, stderr bytes.Buffer
	io := IO{Stdout: &stdout, Stderr: &stderr}

	rc := runRunExec(context.Background(), io, &CLI{Output: "json"}, runCmd{}, d)
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, stderr.String())
	}
	var parsed wire.RunListResult
	if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil {
		t.Fatalf("json parse: %v\nstdout=%s", err, stdout.String())
	}
	if len(parsed.Entries) != 1 || parsed.Entries[0].Name != "ai" {
		t.Errorf("parsed = %+v", parsed)
	}
}

// TestCompatWrap_MethodNotFoundEnrichedWithServerVersion proves the compat
// shim converts lakitu's terse `method_not_found` into an actionable
// message that names the remote lakitu version and this drift's version —
// the "update lakitu" guidance the user is supposed to see instead of
// "method 'run.resolve' not implemented".
func TestCompatWrap_MethodNotFoundEnrichedWithServerVersion(t *testing.T) {
	base := func(_ context.Context, _, method string, _, _ any) error {
		if method == wire.MethodServerVersion {
			t.Fatal("base call should not be invoked for server.version; probe hook handles it")
		}
		return rpcerr.New(rpcerr.CodeUserError, "method_not_found",
			"method %q not implemented", method).With("method", method)
	}
	probe := func(_ context.Context, _ string) (*probeResult, error) {
		return &probeResult{Version: "0.4.1", API: 1}, nil
	}
	wrapped := wrapCallWithCompat(base, probe)

	err := wrapped(context.Background(), "test", wire.MethodRunResolve, nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var re *rpcerr.Error
	if !isRPCErr(err, &re) {
		t.Fatalf("want *rpcerr.Error, got %T: %v", err, err)
	}
	if re.Type != "method_not_found" {
		t.Errorf("Type = %q, want method_not_found (preserved)", re.Type)
	}
	msg := re.Message
	if !strings.Contains(msg, "0.4.1") {
		t.Errorf("message missing server version: %q", msg)
	}
	if !strings.Contains(msg, wire.MethodRunResolve) {
		t.Errorf("message missing method name: %q", msg)
	}
	if !strings.Contains(strings.ToLower(msg), "update lakitu") {
		t.Errorf("message missing actionable 'update lakitu' guidance: %q", msg)
	}
}

// TestCompatWrap_ProbeFailureStillGivesActionableMessage: even when the
// follow-up server.version probe fails (e.g. transport died after the
// real call), the wrapper must still surface an "update lakitu" pointer —
// a bare "method_not_found" in that case would be a usability regression.
func TestCompatWrap_ProbeFailureStillGivesActionableMessage(t *testing.T) {
	base := func(_ context.Context, _, method string, _, _ any) error {
		return rpcerr.New(rpcerr.CodeUserError, "method_not_found",
			"method %q not implemented", method)
	}
	probe := func(_ context.Context, _ string) (*probeResult, error) {
		return nil, rpcerr.Internal("network blew up")
	}
	wrapped := wrapCallWithCompat(base, probe)

	err := wrapped(context.Background(), "test", wire.MethodRunList, nil, nil)
	var re *rpcerr.Error
	if !isRPCErr(err, &re) {
		t.Fatalf("want *rpcerr.Error, got %T: %v", err, err)
	}
	if !strings.Contains(strings.ToLower(re.Message), "update lakitu") {
		t.Errorf("message missing fallback guidance: %q", re.Message)
	}
}

// TestCompatWrap_OtherErrorsUnmodified: the wrapper must only touch
// method_not_found errors. Timeouts, kart_not_found, validation failures,
// and successful calls all pass through verbatim — otherwise every
// remote call path risks changing shape.
func TestCompatWrap_OtherErrorsUnmodified(t *testing.T) {
	cases := []struct {
		name string
		base func(ctx context.Context, circuit, method string, params, out any) error
	}{
		{
			name: "success",
			base: func(_ context.Context, _, _ string, _, _ any) error { return nil },
		},
		{
			name: "kart_not_found",
			base: func(_ context.Context, _, _ string, _, _ any) error {
				return rpcerr.NotFound(rpcerr.TypeKartNotFound, "kart %q", "ghost")
			},
		},
	}
	probeCalled := 0
	probe := func(_ context.Context, _ string) (*probeResult, error) {
		probeCalled++
		return &probeResult{Version: "x"}, nil
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probeCalled = 0
			wrapped := wrapCallWithCompat(tc.base, probe)
			got := wrapped(context.Background(), "c", "m", nil, nil)
			want := tc.base(context.Background(), "c", "m", nil, nil)
			if (got == nil) != (want == nil) {
				t.Fatalf("got=%v want=%v", got, want)
			}
			if probeCalled != 0 {
				t.Errorf("probe called %d times, want 0 (non-not-found path)", probeCalled)
			}
		})
	}
}

// isRPCErr is a tiny errors.As that keeps the test signal local.
func isRPCErr(err error, target **rpcerr.Error) bool {
	for err != nil {
		if re, ok := err.(*rpcerr.Error); ok {
			*target = re
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
