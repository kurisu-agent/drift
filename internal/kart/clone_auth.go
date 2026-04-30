package kart

import "net/url"

// injectGithubPATIntoCloneURL rewrites a github HTTPS/HTTP URL so a
// fine-grained PAT rides on it as basic-auth. devpod's in-container
// `git clone` runs before layer-1 dotfiles install, so this is the
// only auth path that works for the initial clone of a private github
// repo. github accepts any non-empty username paired with a
// fine-grained PAT; the documented convention is `x-access-token`.
//
// Returns ok=false (URL unchanged) for:
//   - empty token. Caller is expected to guard, but defensive here.
//   - URLs that don't parse (caller passed something garbage).
//   - non-HTTP(S) schemes — `ssh://`, `git://`, scp-style
//     `git@host:owner/repo`. These carry their own auth conventions
//     (ssh keys, anonymous read-only) and PAT-injection doesn't apply.
//   - hosts other than github.com (with optional port). GitHub
//     Enterprise lives at custom hostnames; the registry has no model
//     of those yet, so we don't synthesize creds for them.
//   - URLs that already carry userinfo (`https://user@github.com/...`
//     or `https://user:pass@github.com/...`). Caller wired auth
//     themselves; don't overwrite.
//
// The returned URL flows through driftexec.RedactSecrets when echoed
// by devpod.Client and through both the URL-anchor and literal
// github-token patterns when emitted by devpod itself, so the embedded
// token does not surface in operator-visible logs.
func injectGithubPATIntoCloneURL(rawurl, token string) (string, bool) {
	if token == "" {
		return rawurl, false
	}
	u, err := url.Parse(rawurl)
	if err != nil {
		return rawurl, false
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return rawurl, false
	}
	if u.Hostname() != "github.com" {
		return rawurl, false
	}
	if u.User != nil {
		return rawurl, false
	}
	if u.Path == "" || u.Path == "/" {
		return rawurl, false
	}
	u.User = url.UserPassword("x-access-token", token)
	return u.String(), true
}
