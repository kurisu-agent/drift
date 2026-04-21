package drift

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrintManualCleanupDefaultContext(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	printManualCleanup(&buf, migrateCandidate{Name: "research", Context: "default"})
	got := buf.String()
	if !strings.Contains(got, "devpod delete research") {
		t.Errorf("expected plain devpod delete; got:\n%s", got)
	}
	if strings.Contains(got, "--context") {
		t.Errorf("default context should not include --context flag; got:\n%s", got)
	}
}

func TestPrintManualCleanupOtherContext(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	printManualCleanup(&buf, migrateCandidate{Name: "research", Context: "work"})
	got := buf.String()
	if !strings.Contains(got, "devpod --context work delete research") {
		t.Errorf("expected --context flag for non-default; got:\n%s", got)
	}
}

func TestContainsString(t *testing.T) {
	t.Parallel()
	if !containsString([]string{"a", "b", "c"}, "b") {
		t.Error("want true for present value")
	}
	if containsString([]string{"a", "b"}, "z") {
		t.Error("want false for absent value")
	}
	if containsString(nil, "") {
		t.Error("nil slice should not contain anything")
	}
}

func TestOrFallback(t *testing.T) {
	t.Parallel()
	if got := or("x", "fb"); got != "x" {
		t.Errorf("or(x,fb) = %q, want x", got)
	}
	if got := or("", "fb"); got != "fb" {
		t.Errorf("or(\"\",fb) = %q, want fb", got)
	}
}
