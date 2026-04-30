package drift

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kurisu-agent/drift/internal/wire"
)

// TestRunCircuitSetIcon_PushesNerdfontNameToServer asserts the call goes
// through config.set with key=icon — the server is the source of truth,
// the client only validates locally for fast UX.
func TestRunCircuitSetIcon_PushesNerdfontNameToServer(t *testing.T) {
	var gotMethod, gotKey, gotValue string
	d, _ := newKartDeps(t, func(_ context.Context, _, method string, params, _ any) error {
		gotMethod = method
		if p, ok := params.(map[string]string); ok {
			gotKey = p["key"]
			gotValue = p["value"]
		}
		return nil
	})
	var out, errBuf bytes.Buffer
	io := IO{Stdout: &out, Stderr: &errBuf}

	rc := runCircuitSetIcon(context.Background(), io, &CLI{}, circuitSetIconCmd{Icon: "dev-go"}, d)
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, errBuf.String())
	}
	if gotMethod != wire.MethodConfigSet {
		t.Errorf("method = %q, want %q", gotMethod, wire.MethodConfigSet)
	}
	if gotKey != "icon" || gotValue != "dev-go" {
		t.Errorf("params = (key=%q value=%q), want (icon, dev-go)", gotKey, gotValue)
	}
	if !strings.Contains(out.String(), "dev-go") {
		t.Errorf("stdout missing icon name: %q", out.String())
	}
}

func TestRunCircuitSetIcon_AcceptsEmoji(t *testing.T) {
	var gotValue string
	d, _ := newKartDeps(t, func(_ context.Context, _, _ string, params, _ any) error {
		if p, ok := params.(map[string]string); ok {
			gotValue = p["value"]
		}
		return nil
	})
	var out, errBuf bytes.Buffer
	io := IO{Stdout: &out, Stderr: &errBuf}

	rc := runCircuitSetIcon(context.Background(), io, &CLI{}, circuitSetIconCmd{Icon: "🚀"}, d)
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, errBuf.String())
	}
	if gotValue != "🚀" {
		t.Errorf("server got value %q, want emoji literal", gotValue)
	}
}

func TestRunCircuitSetIcon_RejectsUnknownNerdfontName(t *testing.T) {
	called := false
	d, _ := newKartDeps(t, func(_ context.Context, _, _ string, _, _ any) error {
		called = true
		return nil
	})
	var out, errBuf bytes.Buffer
	io := IO{Stdout: &out, Stderr: &errBuf}

	rc := runCircuitSetIcon(context.Background(), io, &CLI{}, circuitSetIconCmd{Icon: "dev-not_real_at_all"}, d)
	if rc == 0 {
		t.Fatalf("expected rejection; stderr=%s", errBuf.String())
	}
	if called {
		t.Error("server was called despite local validation failure")
	}
	if !strings.Contains(errBuf.String(), "unknown nerd-font icon") {
		t.Errorf("stderr = %q, want hint about unknown icon", errBuf.String())
	}
}

func TestRunCircuitIcons_SubstringFiltersAndJSONShape(t *testing.T) {
	var out, errBuf bytes.Buffer
	io := IO{Stdout: &out, Stderr: &errBuf}

	rc := runCircuitIcons(io, &CLI{Output: "json"}, circuitIconsCmd{Query: "dev-go", Limit: 5})
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, errBuf.String())
	}
	var payload struct {
		Query string `json:"query"`
		Total int    `json:"total"`
		Hits  []struct {
			Name  string `json:"name"`
			Glyph string `json:"glyph"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal: %v — body=%s", err, out.String())
	}
	if payload.Total == 0 {
		t.Fatal("expected at least one hit for dev-go")
	}
	if payload.Hits[0].Name != "dev-go" {
		t.Errorf("first hit = %q, want dev-go (exact match should rank first)", payload.Hits[0].Name)
	}
	if payload.Hits[0].Glyph == "" {
		t.Errorf("first hit has empty glyph")
	}
}

// TestRunCircuitSetIcon_BareCallReadsCurrent — bare `drift circuit set icon`
// (no arg) should fetch + print the current icon, not push an empty value.
func TestRunCircuitSetIcon_BareCallReadsCurrent(t *testing.T) {
	var gotMethod string
	d, _ := newKartDeps(t, func(_ context.Context, _, method string, _, out any) error {
		gotMethod = method
		raw := json.RawMessage(`{"version":"x","api":1,"name":"main","icon":"dev-go"}`)
		*(out.(*wire.ServerInfo)) = wire.ServerInfo{Version: "x", API: 1, Name: "main", Icon: "dev-go"}
		_ = raw
		return nil
	})
	var out, errBuf bytes.Buffer
	io := IO{Stdout: &out, Stderr: &errBuf}

	rc := runCircuitSetIcon(context.Background(), io, &CLI{}, circuitSetIconCmd{}, d)
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, errBuf.String())
	}
	if gotMethod != wire.MethodServerInfo {
		t.Errorf("method = %q, want server.info (bare-call should be a read)", gotMethod)
	}
	if !strings.Contains(out.String(), "dev-go") {
		t.Errorf("stdout missing current icon name: %q", out.String())
	}
}
