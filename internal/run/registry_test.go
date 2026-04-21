package run_test

import (
	"strings"
	"testing"

	"github.com/kurisu-agent/drift/internal/run"
)

func TestParse_minimal(t *testing.T) {
	buf := []byte(`
runs:
  ai:
    description: "Claude"
    mode: interactive
    command: 'exec claude'
  uptime:
    mode: output
    command: 'uptime'
`)
	reg, err := run.Parse(buf)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	ai, ok := reg.Get("ai")
	if !ok {
		t.Fatalf("missing ai entry")
	}
	if ai.Mode != run.ModeInteractive {
		t.Errorf("ai.Mode = %q, want interactive", ai.Mode)
	}
	if ai.Command != "exec claude" {
		t.Errorf("ai.Command = %q", ai.Command)
	}
	if ai.Name != "ai" {
		t.Errorf("ai.Name = %q, want ai", ai.Name)
	}
	sorted := reg.Sorted()
	if len(sorted) != 2 || sorted[0].Name != "ai" || sorted[1].Name != "uptime" {
		t.Errorf("sorted order wrong: %+v", sorted)
	}
}

func TestParse_rejectsMissingMode(t *testing.T) {
	buf := []byte(`runs:
  bad:
    command: 'echo hi'`)
	if _, err := run.Parse(buf); err == nil || !strings.Contains(err.Error(), "mode required") {
		t.Fatalf("want mode-required error, got %v", err)
	}
}

func TestParse_rejectsUnknownPost(t *testing.T) {
	buf := []byte(`runs:
  bad:
    mode: interactive
    post: totally-bogus
    command: 'true'`)
	if _, err := run.Parse(buf); err == nil || !strings.Contains(err.Error(), "unknown post hook") {
		t.Fatalf("want unknown-post error, got %v", err)
	}
}

func TestParse_rejectsBadName(t *testing.T) {
	buf := []byte(`runs:
  "BadName":
    mode: output
    command: 'true'`)
	if _, err := run.Parse(buf); err == nil || !strings.Contains(err.Error(), "invalid entry name") {
		t.Fatalf("want invalid-name error, got %v", err)
	}
}

func TestLoad_missingFileIsEmpty(t *testing.T) {
	reg, err := run.Load(t.TempDir() + "/nope.yaml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(reg.Entries) != 0 {
		t.Errorf("want empty registry, got %d entries", len(reg.Entries))
	}
}
