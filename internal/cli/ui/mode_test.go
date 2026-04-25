package ui

import (
	"bytes"
	"os"
	"testing"
)

func TestDetectMode(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("DRIFT_NO_TUI", "")
	cases := []struct {
		name   string
		stdout any
		flags  ModeFlags
		env    map[string]string
		want   Mode
	}{
		{"json flag", &bytes.Buffer{}, ModeFlags{JSON: true}, nil, ModeJSON},
		{"non-tty plain", &bytes.Buffer{}, ModeFlags{}, nil, ModePlain},
		{"non-tty wantTUI", &bytes.Buffer{}, ModeFlags{WantTUI: true}, nil, ModePlain},
		{"NO_COLOR forces plain", &bytes.Buffer{}, ModeFlags{}, map[string]string{"NO_COLOR": "1"}, ModePlain},
		{"NoColor flag", &bytes.Buffer{}, ModeFlags{NoColor: true}, nil, ModePlain},
		{"json beats nocolor", &bytes.Buffer{}, ModeFlags{JSON: true, NoColor: true}, nil, ModeJSON},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			w, _ := tc.stdout.(*bytes.Buffer)
			got := DetectMode(w, w, tc.flags)
			if got != tc.want {
				t.Fatalf("DetectMode = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNoColorTheme(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf bytes.Buffer
	tm := NewTheme(&buf, false)
	if tm.Enabled {
		t.Fatalf("NO_COLOR=1: theme should be disabled")
	}
	if got := tm.Success("hello"); got != "hello" {
		t.Fatalf("Success on disabled theme should return raw input, got %q", got)
	}
	tm.SuccessLine(&buf, "ok")
	if bytes.ContainsRune(buf.Bytes(), 0x1b) {
		t.Fatalf("ANSI escape leaked under NO_COLOR=1: %q", buf.String())
	}
}

func TestNerdFontOptOut(t *testing.T) {
	t.Setenv("DRIFT_NO_NERDFONT", "1")
	if NerdFont() {
		t.Fatalf("DRIFT_NO_NERDFONT=1: NerdFont() should be false")
	}
	if Icon(IconSuccess) != "ok" {
		t.Fatalf("ascii fallback for IconSuccess want %q got %q", "ok", Icon(IconSuccess))
	}
}

// Ensure os package is referenced even if test runner skips this file.
var _ = os.Stdout
