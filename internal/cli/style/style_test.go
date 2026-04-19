package style_test

import (
	"bytes"
	"strings"
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

func TestDisabled_ReturnsInputsUnchanged(t *testing.T) {
	p := style.Disabled()
	for _, s := range []string{"", "x", "colored\ttext"} {
		if p.Success(s) != s || p.Warn(s) != s || p.Error(s) != s ||
			p.Dim(s) != s || p.Accent(s) != s || p.Bold(s) != s {
			t.Errorf("Disabled palette altered %q", s)
		}
	}
}

func TestNilReceiver_IsNoOp(t *testing.T) {
	var p *style.Palette
	if got := p.Accent("x"); got != "x" {
		t.Errorf("nil palette Accent = %q, want %q", got, "x")
	}
}

func TestStripANSI(t *testing.T) {
	in := "\x1b[31merror\x1b[0m: \x1b[1;32mgreen\x1b[0m tail"
	want := "error: green tail"
	if got := style.StripANSI(in); got != want {
		t.Errorf("StripANSI = %q, want %q", got, want)
	}
	if got := style.StripANSI("plain"); got != "plain" {
		t.Errorf("StripANSI plain altered: %q", got)
	}
}

func TestStripANSI_HandlesCursorMoves(t *testing.T) {
	in := "\x1b[2K\x1b[1Gline\n"
	if !strings.Contains(style.StripANSI(in), "line\n") {
		t.Errorf("StripANSI dropped content: %q", style.StripANSI(in))
	}
}
