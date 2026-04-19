package exec_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	driftexec "github.com/kurisu-agent/drift/internal/exec"
)

func TestRunCapturesStdoutAndStderrSeparately(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	// /bin/sh -c here is a test-only convenience for emitting known
	// bytes to stdout and stderr; the package API forbids shell
	// invocation for real callers. See TestNoShellInvocationInSources.
	res, err := driftexec.Run(ctx, driftexec.Cmd{
		Name: "/bin/sh",
		Args: []string{"-c", "printf hello; printf err 1>&2"},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if got, want := string(res.Stdout), "hello"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	if got, want := string(res.Stderr), "err"; got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", res.ExitCode)
	}
}

func TestRunReturnsTypedErrorOnNonZeroExit(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	_, err := driftexec.Run(ctx, driftexec.Cmd{
		Name: "/bin/sh",
		Args: []string{"-c", "printf 'boom\\nline two\\n' 1>&2; exit 3"},
	})
	if err == nil {
		t.Fatal("Run returned nil error, expected *Error")
	}

	var execErr *driftexec.Error
	if !errors.As(err, &execErr) {
		t.Fatalf("err = %v (%T), want *driftexec.Error", err, err)
	}
	if execErr.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3", execErr.ExitCode)
	}
	if execErr.FirstStderrLine != "boom" {
		t.Errorf("FirstStderrLine = %q, want %q", execErr.FirstStderrLine, "boom")
	}
	if !strings.Contains(string(execErr.Stderr), "line two") {
		t.Errorf("Stderr = %q, want to contain 'line two'", execErr.Stderr)
	}
	if !strings.Contains(err.Error(), "exited 3") || !strings.Contains(err.Error(), "boom") {
		t.Errorf("Error() = %q, want to contain 'exited 3' and 'boom'", err.Error())
	}
}

func TestRunContextCancelTerminatesChildPromptly(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())

	done := make(chan error, 1)
	go func() {
		_, err := driftexec.Run(ctx, driftexec.Cmd{
			Name:      "/bin/sh",
			Args:      []string{"-c", "sleep 30"},
			WaitDelay: 2 * time.Second,
		})
		done <- err
	}()

	// Give the child a beat to actually start before cancelling.
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run returned nil after cancel, expected ctx error")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want errors.Is(ctx.Canceled)", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s of cancel; WaitDelay budget blown")
	}
}

func TestRunSIGKILLAfterWaitDelayOnHungChild(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())

	// A process that ignores SIGTERM forces the SIGKILL escalation path.
	// `trap '' TERM` + sleep is the canonical way to stage that.
	start := time.Now()
	done := make(chan error, 1)
	go func() {
		_, err := driftexec.Run(ctx, driftexec.Cmd{
			Name:      "/bin/sh",
			Args:      []string{"-c", "trap '' TERM; sleep 30"},
			WaitDelay: 500 * time.Millisecond,
		})
		done <- err
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		elapsed := time.Since(start)
		if err == nil {
			t.Fatal("Run returned nil after cancel, expected ctx error")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want errors.Is(ctx.Canceled)", err)
		}
		// Budget: 100ms pre-cancel + 500ms WaitDelay + slack.
		if elapsed > 3*time.Second {
			t.Errorf("Run took %s to reap SIGTERM-ignoring child; budget is WaitDelay + slack", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return; SIGKILL escalation appears broken")
	}
}

func TestRunDefaultWaitDelayIsFiveSeconds(t *testing.T) {
	t.Parallel()
	if driftexec.DefaultWaitDelay != 5*time.Second {
		t.Errorf("DefaultWaitDelay = %s, want 5s", driftexec.DefaultWaitDelay)
	}
}

func TestRunRejectsEmptyName(t *testing.T) {
	t.Parallel()
	_, err := driftexec.Run(t.Context(), driftexec.Cmd{})
	if err == nil {
		t.Fatal("Run with empty Name returned nil, expected error")
	}
}

func TestRunPropagatesStdin(t *testing.T) {
	t.Parallel()
	res, err := driftexec.Run(t.Context(), driftexec.Cmd{
		Name:  "/bin/cat",
		Stdin: strings.NewReader("piped-in"),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := string(res.Stdout); got != "piped-in" {
		t.Errorf("stdout = %q, want %q", got, "piped-in")
	}
}

// TestNoShellInvocationInSources enforces the "no shell" invariant: the
// exec package itself must never construct an argv that invokes a shell.
// It walks the package's non-test .go files and fails if any exec.Command*
// call site mentions "sh" or "bash".
func TestNoShellInvocationInSources(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	var sources []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		sources = append(sources, name)
	}
	if len(sources) == 0 {
		t.Fatal("no non-test .go files found in package directory")
	}

	for _, path := range sources {
		src, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			t.Fatalf("ReadFile %s: %v", path, err)
		}
		text := string(src)
		// The package uses osexec (alias) / exec.CommandContext with a
		// caller-supplied Name. A literal "sh" or "bash" token appearing
		// anywhere in the source would mean someone baked a shell in.
		for _, banned := range []string{`"sh"`, `"bash"`, `"/bin/sh"`, `"/bin/bash"`} {
			if strings.Contains(text, banned) {
				t.Errorf("%s: contains forbidden shell literal %s", path, banned)
			}
		}
		// Also guard against the classic `sh -c` / `bash -c` argv shape
		// appearing inline near a Command* call.
		lines := strings.Split(text, "\n")
		for i, line := range lines {
			lower := strings.ToLower(line)
			if !strings.Contains(lower, "command(") && !strings.Contains(lower, "commandcontext(") {
				continue
			}
			window := strings.Join(lines[i:min(i+6, len(lines))], " ")
			if strings.Contains(window, "\"-c\"") && (strings.Contains(window, "sh") || strings.Contains(window, "bash")) {
				t.Errorf("%s: line %d appears to invoke a shell with -c", path, i+1)
			}
		}
	}
}

// Sanity: make sure the file layout assumed by TestNoShellInvocationInSources
// matches reality. If someone renames exec.go this test will catch it before
// the grep test silently passes over an empty file list.
func TestPackageFileExists(t *testing.T) {
	t.Parallel()
	if _, err := os.Stat(filepath.Join(".", "exec.go")); err != nil {
		t.Fatalf("expected exec.go to exist in package dir: %v", err)
	}
}

func TestStderrTail_ReturnsEmptyForNonExecError(t *testing.T) {
	t.Parallel()
	if got := driftexec.StderrTail(errors.New("plain")); got != "" {
		t.Errorf("StderrTail(plain) = %q, want empty", got)
	}
	if got := driftexec.StderrTail(nil); got != "" {
		t.Errorf("StderrTail(nil) = %q, want empty", got)
	}
}

func TestStderrTail_StripsANSIAndRedactsSecrets(t *testing.T) {
	t.Parallel()
	e := &driftexec.Error{
		Stderr: []byte(
			"\x1b[31mwarn\x1b[0m resolving deps\n" +
				"GET https://user:supersecret@example.com/repo.git\n" +
				"Authorization: Bearer abcd1234\n" +
				"token=xyz123 in log\n" +
				"fatal: auth failed\n"),
	}
	out := driftexec.StderrTail(e)
	if strings.Contains(out, "\x1b[") {
		t.Errorf("ANSI not stripped: %q", out)
	}
	if strings.Contains(out, "supersecret") {
		t.Errorf("URL credentials leaked: %q", out)
	}
	if strings.Contains(out, "Bearer abcd1234") {
		t.Errorf("Authorization value leaked: %q", out)
	}
	if strings.Contains(out, "token=xyz123") {
		t.Errorf("token=value leaked: %q", out)
	}
	if !strings.Contains(out, "fatal: auth failed") {
		t.Errorf("tail missing final line: %q", out)
	}
}

func TestStderrTail_CapsToLastLines(t *testing.T) {
	t.Parallel()
	var lines []string
	for i := 0; i < 40; i++ {
		lines = append(lines, "line"+strings.Repeat("x", 20))
	}
	e := &driftexec.Error{Stderr: []byte(strings.Join(lines, "\n"))}
	out := driftexec.StderrTail(e)
	outLines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(outLines) > 20 {
		t.Errorf("got %d lines, want <= 20", len(outLines))
	}
	if !strings.HasSuffix(out, lines[len(lines)-1]) {
		t.Errorf("last line not preserved: %q", out)
	}
}
