package ui

import (
	"bytes"
	"strings"
	"testing"
)

func TestSpinnerDisabledNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf bytes.Buffer
	tm := NewTheme(&buf, false)
	sp := tm.NewSpinner(&buf, SpinnerOptions{Message: "doing"})
	sp.Succeed("done")
	got := buf.String()
	if strings.ContainsRune(got, 0x1b) {
		t.Fatalf("ANSI in disabled spinner output: %q", got)
	}
	if !strings.Contains(got, "done") {
		t.Fatalf("Succeed final message missing: %q", got)
	}
}

func TestPhaseTrackerDisabled(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf bytes.Buffer
	tm := NewTheme(&buf, false)
	pt := tm.NewPhaseTracker(&buf, []string{"clone", "up", "dotfiles"})
	pt.Begin("clone")
	pt.Begin("up")
	pt.Done("dotfiles")
	got := buf.String()
	if strings.ContainsRune(got, 0x1b) {
		t.Fatalf("ANSI under NO_COLOR: %q", got)
	}
}
