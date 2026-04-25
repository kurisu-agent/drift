package kart

import (
	"context"
	"strings"
	"testing"

	"github.com/kurisu-agent/drift/internal/seed"
)

// TestSeedFragmentsEmptyWhenNoSeeds — the common path: tune without a
// seed: list produces no shell. dp is intentionally nil because the
// no-seeds branch must short-circuit before any SSH read.
func TestSeedFragmentsEmptyWhenNoSeeds(t *testing.T) {
	got, err := seedFragments(context.Background(), nil, &Resolved{Name: "kart-x"})
	if err != nil {
		t.Fatalf("seedFragments: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty fragment, got %q", got)
	}
}

// TestSeedFileFragmentOverwrite — default mode emits the canonical
// mkdir + symlink-break + base64 write, no skip gate.
func TestSeedFileFragmentOverwrite(t *testing.T) {
	got := seedFileFragment(seed.RenderedFile{
		Path:          "~/.claude/CLAUDE.md",
		Content:       []byte("hi\n"),
		BreakSymlinks: true,
	}, seed.ConflictOverwrite)
	for _, want := range []string{
		`mkdir -p "$HOME/.claude"`,
		`dst="$HOME/.claude/CLAUDE.md"`,
		`if [ -L "$dst" ]; then rm -f "$dst"; fi`,
		`base64 -d > "$dst"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("overwrite fragment missing %q\n%s", want, got)
		}
	}
	if strings.Contains(got, "if [ ! -e") {
		t.Errorf("overwrite fragment should not have skip gate:\n%s", got)
	}
}

// TestSeedFileFragmentSkip — skip mode wraps the write in an
// if-not-exists guard so a host bind-mount can win.
func TestSeedFileFragmentSkip(t *testing.T) {
	got := seedFileFragment(seed.RenderedFile{
		Path:    "~/x.json",
		Content: []byte("{}"),
	}, seed.ConflictSkip)
	if !strings.Contains(got, `if [ ! -e "$HOME/x.json" ]; then`) {
		t.Errorf("expected absent-guard, got:\n%s", got)
	}
	if !strings.HasSuffix(strings.TrimSpace(got), "fi") {
		t.Errorf("expected closing fi, got:\n%s", got)
	}
}

// TestSeedFileFragmentAppend — append mode uses >> and never breaks
// the destination first (we want to preserve the existing file).
func TestSeedFileFragmentAppend(t *testing.T) {
	got := seedFileFragment(seed.RenderedFile{
		Path:    "~/log.txt",
		Content: []byte("line\n"),
	}, seed.ConflictAppend)
	if !strings.Contains(got, `base64 -d >> "$dst"`) {
		t.Errorf("append fragment missing >>:\n%s", got)
	}
}

// TestSeedFileFragmentPrepend — prepend writes a temp, concatenates
// the existing file (when present), and atomic-renames into place.
func TestSeedFileFragmentPrepend(t *testing.T) {
	got := seedFileFragment(seed.RenderedFile{
		Path:    "~/log.txt",
		Content: []byte("header\n"),
	}, seed.ConflictPrepend)
	for _, want := range []string{
		`mkdir -p "$HOME"`,
		`base64 -d > "$HOME/log.txt.seed-prepend.tmp"`,
		`if [ -e "$HOME/log.txt" ]; then cat "$HOME/log.txt" >> "$HOME/log.txt.seed-prepend.tmp"; fi`,
		`mv "$HOME/log.txt.seed-prepend.tmp" "$HOME/log.txt"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prepend fragment missing %q\n%s", want, got)
		}
	}
}

// TestSeedFragmentsCustomTemplate — a user-defined template carries
// its rendered Path/Content + flags through seedFragments without
// the kart layer caring about the source. Uses overwrite mode (the
// default) so no SSH read is needed.
func TestSeedFragmentsCustomTemplate(t *testing.T) {
	tmpl := &seed.Template{
		Name: "custom",
		Files: []seed.File{
			{Path: "~/notes/{{ .Kart }}.txt", Content: "workspace={{ .Workspace }}\n"},
		},
	}
	got, err := seedFragments(context.Background(), nil,
		&Resolved{Name: "demo", Seeds: []*seed.Template{tmpl}})
	if err != nil {
		t.Fatalf("seedFragments: %v", err)
	}
	if !strings.Contains(got, `mkdir -p "$HOME/notes"`) {
		t.Errorf("expected mkdir of templated parent dir; got:\n%s", got)
	}
	if !strings.Contains(got, `dst="$HOME/notes/demo.txt"`) {
		t.Errorf("expected templated destination; got:\n%s", got)
	}
}
