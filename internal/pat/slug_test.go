package pat

import "testing"

func TestSlugify(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"Test PAT", "test-pat", false},
		{"  My Cool Token!  ", "my-cool-token", false},
		{"already-a-slug", "already-a-slug", false},
		{"name__with__lots___of---separators", "name-with-lots-of-separators", false},
		{"   ", "", true},       // pure whitespace
		{"!!!", "", true},       // pure punctuation
		{"123 token", "", true}, // can't start with a digit per name.Pattern
	}
	for _, tc := range cases {
		got, err := Slugify(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("Slugify(%q): want error, got %q", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("Slugify(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Slugify(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
