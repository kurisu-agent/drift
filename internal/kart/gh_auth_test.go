package kart

import (
	"strings"
	"testing"
)

// Identity is now emitted independent of PAT — a character that
// declares GitName/GitEmail but no PAT still gets a `git config
// --global user.*` block so the kart's gitconfig reflects the
// character (and overwrites devpod's host-identity leak), but the
// `gh auth login` block stays gated on PAT.
func TestGHAuthFragmentIdentityWithoutPAT(t *testing.T) {
	t.Parallel()

	got := ghAuthFragment(&Character{
		GitName:    "Example User",
		GitEmail:   "user@example.test",
		GithubUser: "example-user",
	})

	wantPresent := []string{
		`git config --global user.name 'Example User'`,
		`git config --global user.email 'user@example.test'`,
		`git config --global github.user 'example-user'`,
	}
	for _, want := range wantPresent {
		if !strings.Contains(got, want) {
			t.Errorf("PAT-less character should set %q\ngot:\n%s", want, got)
		}
	}
	for _, forbid := range []string{"gh auth login", "gh auth setup-git", "command -v gh"} {
		if strings.Contains(got, forbid) {
			t.Errorf("PAT-less character must not emit %q\ngot:\n%s", forbid, got)
		}
	}
}

// Truly empty inputs still emit nothing — no character, or a character
// with no identity fields and no PAT, has no work to do post-up.
func TestGHAuthFragmentEmptyWhenNothingToDo(t *testing.T) {
	t.Parallel()

	cases := map[string]*Character{
		"nil character":   nil,
		"empty character": {},
	}
	for name, char := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := ghAuthFragment(char); got != "" {
				t.Fatalf("ghAuthFragment(%v) = %q, want empty", char, got)
			}
		})
	}
}

func TestGHAuthFragmentWithPAT(t *testing.T) {
	t.Parallel()

	got := ghAuthFragment(&Character{
		GitName:    "Example User",
		GitEmail:   "user@example.test",
		GithubUser: "example-user",
		PAT:        "github_pat_xxxxxxxxxxxx",
	})

	wants := []string{
		`git config --global user.name 'Example User'`,
		`git config --global user.email 'user@example.test'`,
		`git config --global github.user 'example-user'`,
		`command -v gh`,
		`gh auth login --with-token --hostname github.com`,
		`gh auth setup-git --hostname github.com`,
		// PAT is single-quoted so the printf cannot expand or word-split it.
		`'github_pat_xxxxxxxxxxxx'`,
	}
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Errorf("ghAuthFragment with PAT missing %q\ngot:\n%s", want, got)
		}
	}
}

func TestGHAuthFragmentSkipsMissingIdentityFields(t *testing.T) {
	t.Parallel()

	// PAT alone is enough to emit the gh-auth block; identity lines
	// are gated on their respective fields. Verifies a kart-level PAT
	// applied to a bare character still wires gh.
	got := ghAuthFragment(&Character{PAT: "github_pat_xx"})

	if !strings.Contains(got, "gh auth login --with-token") {
		t.Errorf("PAT-only character should still emit gh auth login\ngot:\n%s", got)
	}
	for _, forbid := range []string{"user.name", "user.email", "github.user"} {
		if strings.Contains(got, forbid) {
			t.Errorf("PAT-only character must not emit %q\ngot:\n%s", forbid, got)
		}
	}
}

func TestShellSingleQuoteEscapesEmbeddedQuote(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"":            `''`,
		"plain":       `'plain'`,
		"with space":  `'with space'`,
		"with $var":   `'with $var'`,
		`O'Brien`:     `'O'\''Brien'`,
		`a'b'c`:       `'a'\''b'\''c'`,
		`back\slash`:  `'back\slash'`,
		"with\nnewln": "'with\nnewln'",
	}
	for in, want := range cases {
		if got := shellSingleQuote(in); got != want {
			t.Errorf("shellSingleQuote(%q) = %q, want %q", in, got, want)
		}
	}
}
