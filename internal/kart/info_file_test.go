package kart

import (
	"testing"
)

// TestTuneIconResolvesCatalogNames is a thin guard that we did not break
// the icons.Resolve hookup — the kart info path is the only place a
// nerd-font name in a tune YAML gets turned into a renderable glyph,
// and silently passing through the raw name (instead of resolving) is
// invisible to the user until they look at the topbar.
func TestTuneIconResolvesCatalogNames(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string // exact match; empty input → empty output
	}{
		{"empty stays empty", "", ""},
		{"emoji passes through", "🧪", "🧪"},
		{"raw codepoint passes through", "", ""},
		// `dev-go` is a stable entry in the upstream nerd-font catalog
		// (Devicons set). If the catalog ever drops it, adjust this
		// assertion or pick another known-stable name.
		{"nerd-font name resolves", "dev-go", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tuneIcon(&Tune{Icon: tc.in})
			if got != tc.want {
				t.Errorf("tuneIcon(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
	if tuneIcon(nil) != "" {
		t.Error("tuneIcon(nil) must be empty")
	}
}

// TestCharacterDisplayName covers the fallback chain consumed by the
// in-kart topbar / claude-statusline. A character without display_name
// must surface as the YAML key (so the topbar always reads as
// *something*); a nil character means no character is selected at all
// and must surface as empty (so the topbar can omit the segment).
func TestCharacterDisplayName(t *testing.T) {
	cases := []struct {
		name     string
		c        *Character
		fallback string
		want     string
	}{
		{"nil → empty", nil, "alice", ""},
		{"display_name wins", &Character{DisplayName: "Alice the Great"}, "alice", "Alice the Great"},
		{"falls back to yaml key", &Character{}, "alice", "alice"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := characterDisplayName(tc.c, tc.fallback); got != tc.want {
				t.Errorf("characterDisplayName = %q, want %q", got, tc.want)
			}
		})
	}
}
