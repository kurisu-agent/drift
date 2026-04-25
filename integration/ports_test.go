//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kurisu-agent/drift/integration"
)

// TestDriftPortsAddListRm exercises the workstation-side state file path:
// `drift ports add` → ports.yaml gains an entry; `drift ports list
// --output json` reflects it; `drift ports rm` removes it. The reconcile
// that fires after each mutation will not actually establish a working
// forward in this harness — the wildcard ProxyCommand routes through a
// devpod-shim that doesn't speak `--stdio` — but the plan documents
// reconcile failures as warnings, not fatal, so the state file mutation
// is what we assert on.
//
// Real-forward end-to-end (curl through localhost into a kart-side
// listener) requires the heavyweight realdevpod harness; that lives in
// TestDriftPortsDevcontainerPassthrough.
func TestDriftPortsAddListRm(t *testing.T) {
	ctx := integration.TestCtx(t, 5*time.Minute)

	c, _ := integration.StartReadyCircuit(ctx, t, false)
	kart := c.KartName("ports")

	// Start with an empty state file.
	if got := portsList(ctx, t, c); len(got) != 0 {
		t.Fatalf("expected empty initial list, got %+v", got)
	}

	// Add → expect one explicit entry.
	stdout, stderr, code := c.Drift(ctx, "ports", "add", "9999", "--kart", "test/"+kart)
	if code != 0 {
		t.Fatalf("ports add: exit=%d\nstdout=%q\nstderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "added test/"+kart) {
		t.Fatalf("ports add: missing 'added' line: %q", stdout)
	}
	got := portsList(ctx, t, c)
	if len(got) != 1 {
		t.Fatalf("after add: want 1 kart entry, got %+v", got)
	}
	if got[0].Kart != "test/"+kart {
		t.Errorf("kart key: got %q, want %q", got[0].Kart, "test/"+kart)
	}
	if len(got[0].Forwards) != 1 ||
		got[0].Forwards[0].Local != 9999 ||
		got[0].Forwards[0].Remote != 9999 ||
		got[0].Forwards[0].Source != "explicit" {
		t.Errorf("after add: unexpected forwards %+v", got[0].Forwards)
	}

	// Idempotent re-add — same port, same kart — should not error and
	// should not duplicate the entry.
	stdout, _, _ = c.Drift(ctx, "ports", "add", "9999", "--kart", "test/"+kart)
	if !strings.Contains(stdout, "already configured") {
		t.Errorf("expected idempotent add message, got %q", stdout)
	}
	if got := portsList(ctx, t, c); len(got) != 1 || len(got[0].Forwards) != 1 {
		t.Errorf("idempotent add changed state: %+v", got)
	}

	// Remap workstation port — `drift ports remap REMOTE:LOCAL`.
	stdout, stderr, code = c.Drift(ctx, "ports", "remap", "9999:19999", "--kart", "test/"+kart)
	if code != 0 {
		t.Fatalf("ports remap: exit=%d\nstdout=%q\nstderr=%q", code, stdout, stderr)
	}
	got = portsList(ctx, t, c)
	if len(got) != 1 || len(got[0].Forwards) != 1 ||
		got[0].Forwards[0].Local != 19999 ||
		got[0].Forwards[0].Remote != 9999 {
		t.Errorf("after remap: unexpected forwards %+v", got)
	}

	// Rm → expect empty.
	stdout, stderr, code = c.Drift(ctx, "ports", "rm", "9999", "--kart", "test/"+kart)
	if code != 0 {
		t.Fatalf("ports rm: exit=%d\nstdout=%q\nstderr=%q", code, stdout, stderr)
	}
	if got := portsList(ctx, t, c); len(got) != 0 {
		t.Errorf("after rm: expected empty list, got %+v", got)
	}
}

// portsListEntry mirrors the JSON shape runPortsList emits when --output
// json is set. We unmarshal into our own struct rather than reusing the
// internal one so the test fails loudly if the JSON shape ever drifts.
type portsListEntry struct {
	Kart   string `json:"kart"`
	Master struct {
		Alive bool `json:"alive"`
	} `json:"master"`
	Forwards []struct {
		Local        int    `json:"local"`
		Remote       int    `json:"remote"`
		RemappedFrom int    `json:"remapped_from,omitempty"`
		Source       string `json:"source,omitempty"`
	} `json:"forwards"`
}

func portsList(ctx context.Context, t *testing.T, c *integration.Circuit) []portsListEntry {
	t.Helper()
	stdout, stderr, code := c.Drift(ctx, "--output", "json", "ports", "list")
	if code != 0 {
		t.Fatalf("ports list: exit=%d stderr=%q", code, stderr)
	}
	stdout = strings.TrimSpace(stdout)
	if stdout == "" {
		return nil
	}
	var entries []portsListEntry
	if err := json.Unmarshal([]byte(stdout), &entries); err != nil {
		t.Fatalf("parse list json: %v\nstdout=%q", err, stdout)
	}
	return entries
}

// TestDriftPortsConnectNoForwardsSkips asserts that --no-forwards
// suppresses the BeforeExec hook entirely: ports.yaml stays empty even
// if the resolved devcontainer would have offered forwardPorts. This
// exercises the wire-level plumbing without needing a real container —
// the devpod-shim returns connect-shim-ok and the test only checks the
// workstation-side state file.
func TestDriftPortsConnectNoForwardsSkips(t *testing.T) {
	ctx := integration.TestCtx(t, 5*time.Minute)

	c, _ := integration.StartReadyCircuit(ctx, t, false)
	kart := c.KartName("noforwards")

	// Same minimal shim shape as TestDriftConnectSSH — list reports the
	// kart, status reports Running, ssh prints a sentinel.
	shim := `#!/bin/sh
case "$1" in
  list)
    printf '[{"id":"` + kart + `","source":{"gitRepository":"u"},"provider":{"name":"docker"}}]\n'
    ;;
  status)
    printf '{"state":"Running"}\n'
    ;;
  ssh)
    echo connect-shim-ok
    ;;
esac
exit 0
`
	c.InstallDevpodShim(ctx, shim)

	writeCfg := `set -e
mkdir -p ~/.drift/garage/karts/` + kart + `
cat > ~/.drift/garage/karts/` + kart + `/config.yaml <<'EOF'
repo: u
source_mode: clone
created_at: "2026-04-19T00:00:00Z"
EOF
`
	if err := integration.SSHCommand(ctx, c, "sh", "-c", writeCfg); err != nil {
		t.Fatalf("write kart config: %v", err)
	}

	stdout, stderr, code := c.Drift(ctx, "connect", "--ssh", "--no-forwards", kart)
	if code != 0 {
		t.Fatalf("drift connect --no-forwards: exit=%d\nstdout=%q\nstderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "connect-shim-ok") {
		t.Fatalf("connect did not reach shim: %q", stdout)
	}
	if got := portsList(ctx, t, c); len(got) != 0 {
		t.Errorf("--no-forwards left state: %+v", got)
	}
}
