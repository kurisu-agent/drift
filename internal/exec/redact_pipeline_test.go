package exec

import (
	"bytes"
	"strings"
	"testing"
)

// Mirrors the production wiring: lakitu wraps os.Stderr in RedactingWriter,
// devpod's streamMirror wraps c.Mirror (which IS lakitu's RedactingWriter)
// in ANOTHER RedactingWriter. devpod writes go through the outer one first.
func TestRedactPipeline_NestedWrappers_LeakProbe(t *testing.T) {
	var sink bytes.Buffer
	inner := &RedactingWriter{W: &sink} // lakitu's wrap
	outer := &RedactingWriter{W: inner} // devpod streamMirror's wrap
	leak := "22:08:43 info URL: https://x-access-token:github_pat_11ABCabcdef@github.com/example-org/example-repo tunnelserver.go:422\n"
	if _, err := outer.Write([]byte(leak)); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := sink.String()
	if strings.Contains(got, "github_pat_") {
		t.Fatalf("token leaked through nested redaction: %q", got)
	}
	t.Logf("redacted: %q", got)
}

// Devpod might write the URL in two chunks (no \n between), then \n in a
// later chunk. RedactingWriter is supposed to buffer across writes.
func TestRedactPipeline_SplitChunks_LeakProbe(t *testing.T) {
	var sink bytes.Buffer
	w := &RedactingWriter{W: &sink}
	chunks := []string{
		"22:08:43 info URL: https://x-access-token:",
		"github_pat_11ABCabcdef@github.com/foo/bar",
		" tunnelserver.go:422\n",
	}
	for _, c := range chunks {
		if _, err := w.Write([]byte(c)); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	got := sink.String()
	if strings.Contains(got, "github_pat_") {
		t.Fatalf("token leaked across split writes: %q", got)
	}
	t.Logf("redacted: %q", got)
}

// secretURLRE needs `@` to anchor; secretGithubTokenRE catches the
// literal token shape on its own. Defense-in-depth covers the case
// where devpod (or any other tool) logs a URL whose @host portion is
// stripped or formatted onto a separate line.
func TestRedactPipeline_NoAtAnchor_LeakProbe(t *testing.T) {
	var sink bytes.Buffer
	w := &RedactingWriter{W: &sink}
	leak := "22:08:43 info URL: https://x-access-token:github_pat_11AAM6JLQ0HZSkViKmKXIyabcdef\n"
	if _, err := w.Write([]byte(leak)); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := sink.String()
	if strings.Contains(got, "github_pat_11") {
		t.Fatalf("LEAK: %q", got)
	}
	t.Logf("redacted: %q", got)
}

// All documented GitHub token prefixes get the same literal-pattern
// coverage: classic ghp_, OAuth gho_, server ghs_, refresh ghr_,
// user-to-server ghu_.
func TestRedactPipeline_AllGithubTokenPrefixes(t *testing.T) {
	prefixes := []string{"ghp_", "gho_", "ghs_", "ghr_", "ghu_"}
	for _, pref := range prefixes {
		var sink bytes.Buffer
		w := &RedactingWriter{W: &sink}
		leak := "some line " + pref + "AAAABBBBCCCCDDDDEEEEFFFFGGGG12345 trailing\n"
		if _, err := w.Write([]byte(leak)); err != nil {
			t.Fatalf("write: %v", err)
		}
		got := sink.String()
		if strings.Contains(got, pref+"AAAA") {
			t.Errorf("LEAK %q in: %q", pref, got)
		}
	}
}
