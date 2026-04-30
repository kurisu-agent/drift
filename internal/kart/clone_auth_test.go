package kart

import "testing"

func TestInjectGithubPATIntoCloneURL(t *testing.T) {
	t.Parallel()
	const tok = "github_pat_xxx"
	const auth = "https://x-access-token:" + tok + "@github.com"
	cases := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		// HTTPS canonical forms.
		{"plain", "https://github.com/foo/bar", auth + "/foo/bar", true},
		{"with .git suffix", "https://github.com/foo/bar.git", auth + "/foo/bar.git", true},
		{"with query string", "https://github.com/foo/bar?ref=main", auth + "/foo/bar?ref=main", true},
		{"with fragment", "https://github.com/foo/bar#frag", auth + "/foo/bar#frag", true},
		{"explicit port 443", "https://github.com:443/foo/bar", "https://x-access-token:" + tok + "@github.com:443/foo/bar", true},
		// HTTP — github redirects to https, but PAT auth still passes
		// through correctly via Authorization header on the redirect.
		{"http scheme accepted", "http://github.com/foo/bar", "http://x-access-token:" + tok + "@github.com/foo/bar", true},

		// Non-HTTP(S) schemes — SSH and git protocols use their own auth,
		// not PAT, so passthrough.
		{"ssh+git", "ssh://git@github.com/foo/bar.git", "ssh://git@github.com/foo/bar.git", false},
		{"scp-style ssh", "git@github.com:foo/bar.git", "git@github.com:foo/bar.git", false},
		{"git protocol", "git://github.com/foo/bar", "git://github.com/foo/bar", false},

		// Non-github hosts — registry has no model for GHE etc.
		{"gitlab passthrough", "https://gitlab.com/foo/bar", "https://gitlab.com/foo/bar", false},
		{"github subdomain", "https://api.github.com/foo/bar", "https://api.github.com/foo/bar", false},
		{"github enterprise", "https://github.example.com/foo/bar", "https://github.example.com/foo/bar", false},
		{"random host", "https://example.com/foo/bar", "https://example.com/foo/bar", false},

		// Already-authenticated — caller wired creds themselves, defer.
		{"existing user only", "https://otheruser@github.com/foo/bar", "https://otheruser@github.com/foo/bar", false},
		{"existing user:pass", "https://u:p@github.com/foo/bar", "https://u:p@github.com/foo/bar", false},

		// Path-less / root-only — nothing to clone, defer.
		{"root", "https://github.com/", "https://github.com/", false},
		{"empty path", "https://github.com", "https://github.com", false},

		// Garbage input.
		{"empty string", "", "", false},
		{"just slashes", "://", "://", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := injectGithubPATIntoCloneURL(tc.in, tok)
			if got != tc.want || ok != tc.ok {
				t.Errorf("got (%q, %v), want (%q, %v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestInjectGithubPATIntoCloneURL_EmptyToken(t *testing.T) {
	t.Parallel()
	got, ok := injectGithubPATIntoCloneURL("https://github.com/foo/bar", "")
	if ok || got != "https://github.com/foo/bar" {
		t.Errorf("empty token must passthrough; got (%q, %v)", got, ok)
	}
}
