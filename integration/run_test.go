//go:build integration

package integration_test

import (
	"bytes"
	"encoding/json"
	osexec "os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/kurisu-agent/drift/integration"
	"github.com/kurisu-agent/drift/internal/wire"
)

// TestRun_Ai exercises `drift run ai` end-to-end against a fresh circuit:
//   - `lakitu init` seeds ~/.drift/runs.yaml with the built-in `ai` entry.
//   - `drift run ai --ssh` resolves the entry via run.resolve, then ssh's
//     in with the rendered command (`cd $HOME/.drift && exec claude …`).
//   - A shim `claude` records its cwd + argv so we can assert on both.
//
// --ssh forces the plain ssh branch (no mosh dependency in the test
// container).
func TestRun_Ai(t *testing.T) {
	ctx := integration.TestCtx(t, 5*time.Minute)

	c, _ := integration.StartReadyCircuit(ctx, t, false)

	c.InstallBin(ctx, "claude", `#!/bin/sh
{
  pwd
  printf '%s\n' "$0" "$@"
} > /tmp/claude.log
chmod 0666 /tmp/claude.log
exit 0
`)

	_, stderr, code := c.Drift(ctx, "run", "ai", "--ssh")
	if code != 0 {
		t.Fatalf("drift run ai: exit=%d stderr=%q", code, stderr)
	}

	out := string(c.ExecInContainer(ctx, "cat", "/tmp/claude.log"))
	got := strings.TrimSpace(out)
	if got == "" {
		t.Fatalf("claude shim was never invoked (empty log)\nstderr=%s", stderr)
	}
	lines := strings.Split(got, "\n")
	home := "/home/" + c.User
	wantCwd := home + "/.drift"
	if lines[0] != wantCwd {
		t.Errorf("claude cwd = %q, want %q\nlog=%q", lines[0], wantCwd, got)
	}
	joined := strings.Join(lines[2:], " ")
	if !strings.Contains(joined, "--dangerously-skip-permissions") {
		t.Errorf("claude argv missing --dangerously-skip-permissions: %q", joined)
	}

	mdOut := string(c.ExecInContainer(ctx, "cat", wantCwd+"/CLAUDE.md"))
	if !strings.Contains(mdOut, "circuit — agent context") {
		t.Errorf("CLAUDE.md missing expected header:\n%s", mdOut)
	}
}

// TestRun_ListsBuiltins asserts that `drift runs` returns the entries
// seeded by `lakitu init` — proving there is no embedded client-side
// knowledge of run names.
func TestRun_ListsBuiltins(t *testing.T) {
	ctx := integration.TestCtx(t, 2*time.Minute)

	c, _ := integration.StartReadyCircuit(ctx, t, false)

	stdout, stderr, code := c.Drift(ctx, "runs")
	if code != 0 {
		t.Fatalf("drift runs: exit=%d stderr=%q", code, stderr)
	}
	for _, name := range []string{"ai", "scaffolder", "ping", "uptime", "disk", "mem"} {
		if !strings.Contains(stdout, name) {
			t.Errorf("drift runs output missing %q:\n%s", name, stdout)
		}
	}
}

// TestRun_UnknownAfterEdit appends a new entry to the circuit's runs.yaml
// after the drift client was built, then asserts it appears in `drift runs`
// — the "no embedded list" guarantee we want to preserve as entries grow.
func TestRun_UnknownAfterEdit(t *testing.T) {
	ctx := integration.TestCtx(t, 2*time.Minute)

	c, _ := integration.StartReadyCircuit(ctx, t, false)

	extra := "\n  hello-from-test:\n    description: \"added after client was built\"\n    mode: output\n    command: 'echo hi'\n"
	if err := integration.SSHCommand(ctx, c, "sh", "-c",
		"printf '%s' "+shellSingleQuote(extra)+" >> \"$HOME/.drift/runs.yaml\""); err != nil {
		t.Fatalf("append runs.yaml: %v", err)
	}

	stdout, stderr, code := c.Drift(ctx, "runs")
	if code != 0 {
		t.Fatalf("drift runs: exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "hello-from-test") {
		t.Errorf("appended entry not visible to client:\n%s", stdout)
	}
}

func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// TestRun_PingWithCLIArg locks down the scripted happy path: positional
// CLI args land in the rendered command verbatim. This is the path that
// previously appeared broken to users (`drift run ping` → empty host)
// because no one was prompting for the missing arg.
func TestRun_PingWithCLIArg(t *testing.T) {
	ctx := integration.TestCtx(t, 2*time.Minute)
	c, _ := integration.StartReadyCircuit(ctx, t, false)

	c.InstallBin(ctx, "ping", `#!/bin/sh
printf '%s\n' "$@" > /tmp/ping.log
chmod 0666 /tmp/ping.log
exit 0
`)

	_, stderr, code := c.Drift(ctx, "run", "ping", "203.0.113.7", "--ssh")
	if code != 0 {
		t.Fatalf("drift run ping: exit=%d stderr=%q", code, stderr)
	}
	out := strings.TrimSpace(string(c.ExecInContainer(ctx, "cat", "/tmp/ping.log")))
	if !strings.Contains(out, "203.0.113.7") {
		t.Errorf("ping.log = %q, want 203.0.113.7\nstderr=%s", out, stderr)
	}
}

// TestRun_ListBackfillsArgsOnStaleYAML pins the fix for circuits seeded
// by a pre-v0.5.2 lakitu: runs.yaml lacks args: for the built-in ping
// entry, but the command is untouched, so run.list must surface the
// args the embedded default declares. Otherwise `drift run ping` on a
// TTY skips the prompt and invokes ping with an empty host.
func TestRun_ListBackfillsArgsOnStaleYAML(t *testing.T) {
	ctx := integration.TestCtx(t, 2*time.Minute)
	c, _ := integration.StartReadyCircuit(ctx, t, false)

	// Overwrite runs.yaml with a "stale" registry: ping is present with
	// the untouched command but no args: — the shape lakitu init seeded
	// before 790331a shipped.
	stale := `runs:
  ping:
    description: "Ping a host (drift run ping <host>)"
    mode: output
    command: 'ping -c 4 {{ .Arg 0 | shq }}'
`
	if err := integration.SSHCommand(ctx, c, "sh", "-c",
		"printf '%s' "+shellSingleQuote(stale)+" > \"$HOME/.drift/runs.yaml\""); err != nil {
		t.Fatalf("seed stale runs.yaml: %v", err)
	}

	stdout, stderr, code := c.Drift(ctx, "runs", "--output", "json")
	if code != 0 {
		t.Fatalf("drift runs --output json: exit=%d stderr=%q", code, stderr)
	}
	var lr wire.RunListResult
	if err := json.Unmarshal([]byte(stdout), &lr); err != nil {
		t.Fatalf("parse json: %v\n%s", err, stdout)
	}
	var ping *wire.RunEntry
	for i := range lr.Entries {
		if lr.Entries[i].Name == "ping" {
			ping = &lr.Entries[i]
			break
		}
	}
	if ping == nil {
		t.Fatalf("ping missing from run.list: %+v", lr.Entries)
	}
	if len(ping.Args) != 1 || ping.Args[0].Name != "host" {
		t.Errorf("args not back-filled: %+v", ping.Args)
	}
}

// TestRun_PingPromptsOnTTY spawns `drift run ping` under a real PTY and
// drives the huh-based prompt: wait for the widget title, send a host,
// wait for drift to finish, then assert the circuit-side ping shim saw
// that exact host. End-to-end coverage for the interactive prompt path
// — the one the prior bug silently skipped.
func TestRun_PingPromptsOnTTY(t *testing.T) {
	ctx := integration.TestCtx(t, 3*time.Minute)
	c, _ := integration.StartReadyCircuit(ctx, t, false)

	c.InstallBin(ctx, "ping", `#!/bin/sh
printf '%s\n' "$@" > /tmp/ping.log
chmod 0666 /tmp/ping.log
exit 0
`)

	cmd := osexec.CommandContext(ctx, integration.DriftBin(t),
		"run", "ping", "--ssh")
	cmd.Env = integration.DriftEnv(c)

	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Fatalf("pty.Start: %v", err)
	}
	t.Cleanup(func() { _ = ptmx.Close() })
	// Bubbletea inspects the TTY size at boot; an unset (0x0) PTY makes
	// the renderer collapse the prompt to just a status line, which in
	// turn makes this test flake on "never saw the title" assertions.
	// 80x24 is the de-facto minimum every TUI expects to work against.
	if sizeErr := pty.Setsize(ptmx, &pty.Winsize{Rows: 24, Cols: 80}); sizeErr != nil {
		t.Fatalf("pty.Setsize: %v", sizeErr)
	}

	var output bytes.Buffer
	var outputMu sync.Mutex
	readerDone := make(chan struct{})
	go func() {
		buf := make([]byte, 4096)
		for {
			n, readErr := ptmx.Read(buf)
			if n > 0 {
				outputMu.Lock()
				output.Write(buf[:n])
				outputMu.Unlock()
			}
			if readErr != nil {
				close(readerDone)
				return
			}
		}
	}()

	snapshot := func() string {
		outputMu.Lock()
		defer outputMu.Unlock()
		return output.String()
	}

	if !waitForSubstring(snapshot, "Host to ping", 20*time.Second) {
		_ = cmd.Process.Kill()
		<-readerDone
		t.Fatalf("prompt never rendered. drift output:\n%s", snapshot())
	}

	// Clear the pre-filled default (Ctrl-U kills the textinput buffer),
	// type a sentinel host, submit.
	if _, wErr := ptmx.Write([]byte{0x15}); wErr != nil {
		t.Fatalf("write Ctrl-U: %v", wErr)
	}
	if _, wErr := ptmx.Write([]byte("203.0.113.42\r")); wErr != nil {
		t.Fatalf("write host: %v", wErr)
	}

	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()
	select {
	case err := <-waitErr:
		if err != nil {
			var ee *osexec.ExitError
			if !asExit(err, &ee) {
				t.Fatalf("drift run ping (pty): %v\noutput:\n%s", err, snapshot())
			}
		}
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		<-readerDone
		t.Fatalf("drift run ping (pty) hung. output:\n%s", snapshot())
	}
	<-readerDone

	got := strings.TrimSpace(string(c.ExecInContainer(ctx, "cat", "/tmp/ping.log")))
	if !strings.Contains(got, "203.0.113.42") {
		t.Errorf("ping.log = %q, want 203.0.113.42\ndrift output:\n%s", got, snapshot())
	}
}

// waitForSubstring polls the shared output buffer until `needle` is
// present or the deadline elapses. Bubble Tea renders with lots of
// cursor positioning; a substring match is robust against interleaving.
func waitForSubstring(snapshot func() string, needle string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(snapshot(), needle) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return strings.Contains(snapshot(), needle)
}

func asExit(err error, target **osexec.ExitError) bool {
	for err != nil {
		if ee, ok := err.(*osexec.ExitError); ok {
			*target = ee
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
