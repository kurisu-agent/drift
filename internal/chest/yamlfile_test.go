package chest

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kurisu-agent/drift/internal/rpcerr"
)

func TestYAMLFileRoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "chest"), 0o700); err != nil {
		t.Fatal(err)
	}
	b := NewYAMLFile(dir)

	cases := map[string]string{
		"github-pat": "ghp_secret",
		"weird":      "a'b\"c", // mixed quote styles — YAML handles both.
		"multi": `line1
line2

line4`, // multi-line value must round-trip via block scalar.
	}

	for k, v := range cases {
		if err := b.Set(k, []byte(v)); err != nil {
			t.Fatalf("Set %q: %v", k, err)
		}
	}

	for k, want := range cases {
		got, err := b.Get(k)
		if err != nil {
			t.Fatalf("Get %q: %v", k, err)
		}
		if string(got) != want {
			t.Fatalf("Get %q = %q, want %q", k, got, want)
		}
	}

	names, err := b.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	wantNames := []string{"github-pat", "multi", "weird"}
	if len(names) != len(wantNames) {
		t.Fatalf("List length = %d, want %d: %v", len(names), len(wantNames), names)
	}
	for i, n := range wantNames {
		if names[i] != n {
			t.Fatalf("List[%d] = %q, want %q", i, names[i], n)
		}
	}

	// File mode must be 0600.
	info, err := os.Stat(b.Path())
	if err != nil {
		t.Fatalf("stat secrets.yaml: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("secrets.yaml mode = %o, want 0600", info.Mode().Perm())
	}

	// yaml.v3 picks a block scalar for the multi-line value; a literal
	// newline should appear in the on-disk form rather than `\n` escapes.
	buf, err := os.ReadFile(b.Path())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(buf), "line1\n") {
		t.Fatalf("expected literal newline in on-disk multi-line value, got: %s", buf)
	}

	if err := b.Remove("github-pat"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	_, err = b.Get("github-pat")
	var re *rpcerr.Error
	if !errors.As(err, &re) || re.Type != rpcerr.TypeChestEntryNotFound {
		t.Fatalf("Get after Remove: want chest_entry_not_found, got %v", err)
	}
}
