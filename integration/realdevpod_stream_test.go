//go:build integration

package integration_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kurisu-agent/drift/integration"
)

// ctxWithDeadline is a cleanup-safe context factory: cleanup funcs can't
// register new t.Cleanup handlers, which the harness helper [TestCtx]
// does — so post-test deletion uses this plain context.WithTimeout
// instead and owns its own cancel.
func ctxWithDeadline(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// TestRealDevpodNewStreamsProgressToStderr is the end-to-end proof that
// `drift new` pipes live devpod progress to the client's stderr. It
// reproduces the bug where `driftDebug` was captured in a package-level
// var at process init, which meant DRIFT_DEBUG=1 (set by the drift CLI
// from Kong's `default:"true"`) arrived too late, `remoteArgv` picked
// the non-debug wrap, and both LAKITU_DEBUG and the MirrorStderr wiring
// stayed off — the whole "show me the build output" chain silently
// no-op'd.
//
// The test uses real devpod (same setup as TestRealDevpodUpAndDelete)
// so a regression in any layer — the client debug capture, the SSH
// transport's MirrorStderr, lakitu's mirror wiring, or devpod's own
// stream behavior — surfaces as a failure here. Pure unit tests can't
// catch mis-ordering across those four layers.
func TestRealDevpodNewStreamsProgressToStderr(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-devpod streaming E2E in -short mode")
	}

	ctx := integration.TestCtx(t, 6*time.Minute)

	c, _ := integration.StartReadyCircuit(ctx, t, false)

	starterURL := c.StageStarter(ctx, "stream-starter", map[string]string{
		"README.md":                       "# stream starter\n",
		".devcontainer/devcontainer.json": `{"image":"debian:bookworm-slim"}` + "\n",
	})
	kart := c.KartName("stream")

	// Default: --debug is on (Kong `default:"true"`). The Drift() helper
	// doesn't pass --no-debug, so this is the same shape as a user typing
	// `drift new foo --starter …` in their shell.
	stdout, stderr, code := c.Drift(ctx, "new", kart,
		"--tune", "none",
		"--starter", starterURL,
	)
	// Cleanup runs regardless of assertion outcome so the next test starts
	// from a clean slate.
	t.Cleanup(func() {
		bg, cancel := ctxWithDeadline(2 * time.Minute)
		defer cancel()
		_, _, _ = c.Drift(bg, "delete", "-y", kart)
	})

	if code != 0 {
		t.Fatalf("drift new: code=%d\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}

	// Assertion 1: stderr must contain the argv echo lakitu writes to its
	// Mirror before each devpod spawn (`→ /…/devpod up …`). Its presence
	// on the client's stderr proves three things end-to-end:
	//   (a) drift set DRIFT_DEBUG=1 in time for the SSH transport to see it
	//   (b) SSH transport wired MirrorStderr = os.Stderr
	//   (c) lakitu's Mirror is wired to its own stderr (so SSH picks it up)
	// Lakitu-side tests can cover (c) alone; this one covers all three.
	// The echo uses c.Binary, which lakitu resolves to an absolute path
	// (e.g. "/usr/local/bin/devpod"), so assert on `devpod up` without
	// locking the path — matches both the pinned-binary path and any
	// future integration harness that swaps the binary location.
	if !strings.Contains(stderr, "devpod up") {
		t.Errorf("stderr missing devpod argv echo — streaming pipeline broken\nstderr=%s", stderr)
	}

	// Assertion 2: stderr carries more than a handful of bytes. A broken
	// streaming path typically leaves stderr nearly empty (maybe an SSH
	// banner, nothing more). Real devpod output is multiple KB minimum.
	// Threshold chosen conservatively — if devpod's output format ever
	// shrinks below 200 bytes for a successful `up`, this will flag it
	// and a maintainer can retune.
	const minStderrBytes = 200
	if len(stderr) < minStderrBytes {
		t.Errorf("stderr too small (%d bytes, want ≥%d) — progress likely not streaming\nstderr=%q",
			len(stderr), minStderrBytes, stderr)
	}

	// Assertion 3: the argv echo must precede the client-side success
	// message that `runNew` emits on stdout. We can't compare timestamps
	// across two separate streams post-hoc, but we CAN assert the
	// `→ devpod` token appears in stderr at all — combined with the
	// `drift new: code=0` above, that means:
	//   - devpod actually ran on the circuit (otherwise no echo)
	//   - its output reached the local stderr before the RPC response
	//     closed the SSH channel (otherwise we'd have a partial pipe)
	// Explicit `up` check makes the assertion specific to the kart.new
	// codepath, not to any incidental devpod call that might happen on
	// other RPCs in the future.
	// The `→ ` arrow prefix combined with `devpod up` is lakitu's signature
	// for the kart.new codepath specifically (see devpod.Client.echoArgv).
	// Asserting on both fragments keeps this test tied to kart.new rather
	// than any incidental devpod call a future RPC might add.
	if !strings.Contains(stderr, "→ ") || !strings.Contains(stderr, "devpod up") {
		t.Errorf("stderr missing `→ … devpod up` echo; kart.new streaming may have regressed\nstderr=%s", stderr)
	}
}

// TestRealDevpodNewStaysQuietWithNoDebug is the symmetric negative: when
// the user opts out with `--no-debug`, the client must NOT mirror SSH
// stderr. This guards against a lazy fix that flips the mirror on
// unconditionally — doing so would leak devpod progress into the stderr
// of anyone piping drift into a script that expects a clean stderr.
func TestRealDevpodNewStaysQuietWithNoDebug(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-devpod --no-debug E2E in -short mode")
	}

	ctx := integration.TestCtx(t, 6*time.Minute)

	c, _ := integration.StartReadyCircuit(ctx, t, false)
	starterURL := c.StageStarter(ctx, "quiet-starter", map[string]string{
		"README.md":                       "# quiet starter\n",
		".devcontainer/devcontainer.json": `{"image":"debian:bookworm-slim"}` + "\n",
	})
	kart := c.KartName("quiet")

	stdout, stderr, code := c.Drift(ctx, "--no-debug", "new", kart,
		"--tune", "none",
		"--starter", starterURL,
	)
	t.Cleanup(func() {
		bg, cancel := ctxWithDeadline(2 * time.Minute)
		defer cancel()
		_, _, _ = c.Drift(bg, "--no-debug", "delete", "-y", kart)
	})

	if code != 0 {
		t.Fatalf("drift new: code=%d\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}

	// Under --no-debug the argv echo must be suppressed. Match on the
	// `→ ` arrow + `devpod up` combo used by the positive test so the
	// two tests diverge on the gating logic alone, not on pattern shape.
	if strings.Contains(stderr, "→ ") && strings.Contains(stderr, "devpod up") {
		t.Errorf("stderr contains devpod echo under --no-debug (streaming must be gated)\nstderr=%s", stderr)
	}
}
