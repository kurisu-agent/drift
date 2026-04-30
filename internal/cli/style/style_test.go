package style_test

import (
	"bytes"
	"testing"

	"github.com/kurisu-agent/drift/internal/cli/style"
)

func TestFor_NonTTYWriterIsDisabled(t *testing.T) {
	var buf bytes.Buffer
	p := style.For(&buf, false)
	if p.Enabled {
		t.Fatal("palette enabled for bytes.Buffer (non-TTY)")
	}
	if got := p.Accent("kart"); got != "kart" {
		t.Errorf("Accent = %q, want %q", got, "kart")
	}
	if got := p.Error("boom"); got != "boom" {
		t.Errorf("Error = %q, want %q", got, "boom")
	}
}

func TestFor_JSONModeDisables(t *testing.T) {
	var buf bytes.Buffer
	p := style.For(&buf, true)
	if p.Enabled {
		t.Fatal("palette enabled with jsonMode=true")
	}
}

// Callers relied on style.Disabled() for a guaranteed no-op palette; after
// the cluster-10 removal, style.For(w, false) with a non-TTY writer is the
// canonical way to get one. This test guards that every styled method
// stays a pass-through in that mode.
func TestFor_NonTTY_AllMethodsPassThrough(t *testing.T) {
	var buf bytes.Buffer
	p := style.For(&buf, false)
	for _, s := range []string{"", "x", "colored\ttext"} {
		if p.Success(s) != s || p.Warn(s) != s || p.Error(s) != s ||
			p.Dim(s) != s || p.Accent(s) != s || p.Bold(s) != s {
			t.Errorf("non-TTY palette altered %q", s)
		}
	}
}

func TestNilReceiver_IsNoOp(t *testing.T) {
	var p *style.Palette
	if got := p.Accent("x"); got != "x" {
		t.Errorf("nil palette Accent = %q, want %q", got, "x")
	}
}
