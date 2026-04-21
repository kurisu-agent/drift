package drift

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestStdinIsTTY_RejectsDevNull is the regression this package existed
// to catch and didn't. Go's os/exec sets Stdin to /dev/null when the
// caller leaves it nil; /dev/null is a character device, so the old
// `Mode() & os.ModeCharDevice` check returned true and every CI-run
// drift invocation was mistaken for an interactive terminal. The
// downstream effect was `drift new`'s auto-connect firing and blowing
// up the whole integration suite with stale_kart.
func TestStdinIsTTY_RejectsDevNull(t *testing.T) {
	t.Parallel()
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Skipf("open %s: %v", os.DevNull, err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if stdinIsTTY(f) {
		t.Errorf("stdinIsTTY(%s) = true, want false — /dev/null is a char device but not a terminal", os.DevNull)
	}
}

// TestStdoutIsTTY_RejectsDevNull mirrors the stdin case. stdoutIsTTY
// is only used by the no-arg interactive menu, but the same bug class
// applies — a service or test harness leaving Stdout unset would have
// drift drop into huh.Select and then hang.
func TestStdoutIsTTY_RejectsDevNull(t *testing.T) {
	t.Parallel()
	f, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Skipf("open %s for write: %v", os.DevNull, err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if stdoutIsTTY(f) {
		t.Errorf("stdoutIsTTY(%s) = true, want false", os.DevNull)
	}
}

// TestStdinIsTTY_RegularFile pins the behavior for a plain-file stdin
// (shell redirection: `drift new < config.txt`). Should not count as
// interactive either.
func TestStdinIsTTY_RegularFile(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "stdin.txt")
	if err := os.WriteFile(p, []byte("hi\n"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatalf("open %s: %v", p, err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if stdinIsTTY(f) {
		t.Errorf("stdinIsTTY(regular file) = true, want false")
	}
}

// TestStdinIsTTY_NonFileReader mirrors the original contract kept from
// the mode-bit implementation: a plain reader (e.g. a bytes.Buffer unit
// test stand-in) is treated as non-TTY. Tests that want interactive
// behavior drive the warmup library directly with IsTTY set, so this
// fallback stays honest.
func TestStdinIsTTY_NonFileReader(t *testing.T) {
	t.Parallel()
	if stdinIsTTY(&bytes.Buffer{}) {
		t.Errorf("stdinIsTTY(bytes.Buffer) = true, want false")
	}
}
