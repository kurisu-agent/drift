package seed

import (
	"strings"
	"testing"
)

func TestMergeJSONDeepUnion(t *testing.T) {
	existing := []byte(`{
  "installMethod": "native",
  "userID": "abc",
  "projects": {"/a": {"trusted": true}}
}`)
	patch := []byte(`{
  "hasCompletedOnboarding": true,
  "projects": {"/b": {"trusted": true}}
}`)
	got, err := Merge(MergeFormatJSON, existing, patch)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	gs := string(got)
	for _, want := range []string{
		`"installMethod": "native"`,
		`"userID": "abc"`,
		`"hasCompletedOnboarding": true`,
		`"/a"`,
		`"/b"`,
	} {
		if !strings.Contains(gs, want) {
			t.Errorf("merged JSON missing %q\n%s", want, gs)
		}
	}
}

func TestMergeJSONPatchScalarsWin(t *testing.T) {
	existing := []byte(`{"version": 1, "name": "old"}`)
	patch := []byte(`{"version": 2}`)
	got, _ := Merge(MergeFormatJSON, existing, patch)
	if !strings.Contains(string(got), `"version": 2`) {
		t.Errorf("patch scalar didn't win:\n%s", got)
	}
	if !strings.Contains(string(got), `"name": "old"`) {
		t.Errorf("non-conflicting key dropped:\n%s", got)
	}
}

func TestMergeJSONArrayReplaced(t *testing.T) {
	// Arrays replace wholesale — concat-by-default surprises more than
	// it helps. Append mode is the explicit knob for concat.
	existing := []byte(`{"tags": ["a", "b"]}`)
	patch := []byte(`{"tags": ["c"]}`)
	got, _ := Merge(MergeFormatJSON, existing, patch)
	if !strings.Contains(string(got), `"c"`) || strings.Contains(string(got), `"a"`) {
		t.Errorf("expected tags replaced wholesale, got:\n%s", got)
	}
}

func TestMergeJSONEmptyExisting(t *testing.T) {
	patch := []byte(`{"x": 1}`)
	got, err := Merge(MergeFormatJSON, nil, patch)
	if err != nil {
		t.Fatalf("Merge with empty existing: %v", err)
	}
	if !strings.Contains(string(got), `"x": 1`) {
		t.Errorf("first-write merge didn't yield patch content:\n%s", got)
	}
}

func TestMergeYAMLDeepUnion(t *testing.T) {
	existing := []byte("a: 1\nb:\n  x: keep\n")
	// yaml.v3 quotes the key `y` (YAML-1.1 booleanish) on output, so
	// pick non-aliased keys for the assertion.
	patch := []byte("b:\n  added: yes-please\nc: 3\n")
	got, err := Merge(MergeFormatYAML, existing, patch)
	if err != nil {
		t.Fatalf("Merge YAML: %v", err)
	}
	for _, want := range []string{"a: 1", "x: keep", "added: yes-please", "c: 3"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("YAML merge missing %q\n%s", want, got)
		}
	}
}

func TestMergeFormatFromPath(t *testing.T) {
	cases := map[string]MergeFormat{
		"~/.claude.json": MergeFormatJSON,
		"a/b.YAML":       MergeFormatYAML,
		"x.yml":          MergeFormatYAML,
	}
	for in, want := range cases {
		got, err := MergeFormatFromPath(in)
		if err != nil {
			t.Errorf("MergeFormatFromPath(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("MergeFormatFromPath(%q) = %q, want %q", in, got, want)
		}
	}
	if _, err := MergeFormatFromPath("notes.txt"); err == nil {
		t.Errorf("expected error for unsupported extension")
	}
}
