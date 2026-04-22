package run_test

import (
	"strings"
	"testing"

	"github.com/kurisu-agent/drift/internal/run"
)

func TestRender_positional(t *testing.T) {
	got, err := run.Render(`ping -c 4 {{ .Arg 0 | shq }}`, []string{"1.1.1.1"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got != `ping -c 4 '1.1.1.1'` {
		t.Errorf("got %q", got)
	}
}

func TestRender_missingArgIsEmpty(t *testing.T) {
	got, err := run.Render(`echo {{ .Arg 0 }}`, nil)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got != "echo " {
		t.Errorf("got %q, want %q", got, "echo ")
	}
}

func TestRender_argsTail(t *testing.T) {
	got, err := run.Render(`echo {{ .Args }}`, []string{"foo", "a b", "c'd"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	// shq single-quotes each value; the embedded ' in "c'd" escapes as '\''.
	want := `echo 'foo' 'a b' 'c'\''d'`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRender_shqEscapesQuote(t *testing.T) {
	got, err := run.Render(`f {{ .Arg 0 | shq }}`, []string{`a'b`})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(got, `'a'\''b'`) {
		t.Errorf("missing escaped quote in %q", got)
	}
}

func TestRender_badTemplate(t *testing.T) {
	if _, err := run.Render(`{{ .Nope }}`, nil); err == nil {
		t.Fatal("want template error, got nil")
	}
}
