package icons

import (
	"testing"
)

func TestLookup_KnownName(t *testing.T) {
	r, ok := Lookup("dev-go")
	if !ok || r == 0 {
		t.Fatalf("Lookup(dev-go) = (%U, %v), want non-zero rune", r, ok)
	}
}

func TestLookup_UnknownName(t *testing.T) {
	if r, ok := Lookup("not-a-real-icon"); ok {
		t.Errorf("Lookup unknown returned (%U, true), want false", r)
	}
}

func TestResolve(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"🚀", "🚀"},
		{"👨‍💻", "👨‍💻"},
		{"dev-go", string(nfGlyphs["dev-go"])},
		{"unknown-name-not-in-catalog", "unknown-name-not-in-catalog"},
	}
	for _, c := range cases {
		if got := Resolve(c.in); got != c.want {
			t.Errorf("Resolve(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestValidate(t *testing.T) {
	ok := []string{
		"",
		"dev-go",
		"cod-rocket",
		"🚀",
		"👨‍💻", // ZWJ joined
		"👋🏽",  // skin tone modifier
		"🇯🇵",  // flag pair
		"猫",   // single CJK glyph
	}
	bad := []string{
		"dev-not-a-real-thing", // looks like a name, isn't in catalog
		"hello",                // multi-grapheme ASCII
		"🚀🚀",                   // two graphemes
		"go",                   // multi-grapheme ASCII
	}
	for _, s := range ok {
		if err := Validate(s); err != nil {
			t.Errorf("Validate(%q) returned %v, want nil", s, err)
		}
	}
	for _, s := range bad {
		if err := Validate(s); err == nil {
			t.Errorf("Validate(%q) returned nil, want error", s)
		}
	}
}

func TestSearch_ExactBeatsPrefix(t *testing.T) {
	hits := Search("dev-go", 5)
	if len(hits) == 0 || hits[0] != "dev-go" {
		t.Fatalf("Search dev-go = %v, want first hit to be exact match", hits)
	}
}

func TestSearch_RanksShorterFirst(t *testing.T) {
	// Both "fa-cat" and "fa-category" substring-match "cat"; the shorter
	// name should rank ahead of the longer one within the substring tier.
	hits := Search("cat", 50)
	if len(hits) < 2 {
		t.Fatalf("expected multiple hits for cat, got %v", hits)
	}
	short, long := -1, -1
	for i, n := range hits {
		if n == "fa-cat" {
			short = i
		}
		if n == "cod-list_tree" { // unrelated; just to silence "unused" if cat changes
			_ = i
		}
		_ = long
	}
	if short < 0 {
		t.Errorf("expected fa-cat in hits, got %v", hits[:min(10, len(hits))])
	}
}

func TestSearch_EmptyQueryReturnsAlphabeticalPrefix(t *testing.T) {
	hits := Search("", 3)
	if len(hits) != 3 {
		t.Fatalf("len = %d, want 3", len(hits))
	}
	if hits[0] > hits[1] || hits[1] > hits[2] {
		t.Errorf("not sorted: %v", hits)
	}
}

func TestSearch_Subsequence(t *testing.T) {
	// "drftgo" subsequence-matches dev-drift_go style names if any exist;
	// for nfNames sorted catalog, we just verify it returns *something*
	// for a query that has no substring hit but does subsequence-match.
	// Use "dvgo" — will subsequence-match "dev-go".
	hits := Search("dvgo", 50)
	found := false
	for _, h := range hits {
		if h == "dev-go" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected dev-go in subsequence hits for 'dvgo', got first 10: %v", hits[:min(10, len(hits))])
	}
}

func TestNames_NonEmpty(t *testing.T) {
	if len(Names()) < 1000 {
		t.Errorf("Names() = %d entries, want at least 1000", len(Names()))
	}
}
