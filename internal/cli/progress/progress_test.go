package progress_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/kurisu-agent/drift/internal/cli/progress"
)

func TestStart_NonTTYWriterIsNoOp(t *testing.T) {
	var buf bytes.Buffer
	ph := progress.Start(&buf, false, "creating kart \"alpha\"", "ssh")
	ph.Succeed("created kart \"alpha\"")
	got := buf.String()
	// Disabled mode writes only the final line via Succeed — no spinner,
	// no ANSI.
	if strings.Contains(got, "\x1b[") {
		t.Errorf("ANSI leaked into non-TTY output: %q", got)
	}
	if !strings.Contains(got, "created kart \"alpha\"") {
		t.Errorf("missing final line: %q", got)
	}
	if strings.Contains(got, "✓") {
		t.Errorf("check mark leaked into disabled output: %q", got)
	}
}

func TestStart_JSONModeSkipsBothSpinnerAndSuccessLine(t *testing.T) {
	var buf bytes.Buffer
	// Even on a TTY we'd want JSON mode to stay silent — but here the
	// writer is already non-TTY so we only confirm that Succeed in
	// disabled-with-jsonMode doesn't produce styled output either.
	ph := progress.Start(&buf, true, "creating kart", "ssh")
	ph.Succeed("created kart")
	if strings.Contains(buf.String(), "\x1b[") {
		t.Errorf("ANSI leaked under jsonMode: %q", buf.String())
	}
}

func TestFail_DisabledMode_WritesNothing(t *testing.T) {
	var buf bytes.Buffer
	ph := progress.Start(&buf, false, "connecting", "ssh")
	ph.Fail()
	if buf.Len() != 0 {
		t.Errorf("Fail in disabled mode wrote output: %q", buf.String())
	}
}

func TestSucceed_IsIdempotent(t *testing.T) {
	var buf bytes.Buffer
	ph := progress.Start(&buf, false, "x", "")
	ph.Succeed("done")
	ph.Succeed("done") // second call must not duplicate
	if n := strings.Count(buf.String(), "done"); n != 1 {
		t.Errorf("Succeed emitted %d lines, want 1: %q", n, buf.String())
	}
}
