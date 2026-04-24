// Package name validates kart, circuit, and character identifiers. Tiny
// and dependency-free so both drift and lakitu can import it.
package name

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/kurisu-agent/drift/internal/rpcerr"
)

// Pattern: lowercase alphanumeric + hyphen, 1–63 chars, starts with a letter.
const Pattern = `^[a-z][a-z0-9-]{0,62}$`

var re = regexp.MustCompile(Pattern)

// Reserved collide with tune sentinel values.
var reserved = map[string]struct{}{
	"default": {},
	"none":    {},
}

func Valid(s string) bool {
	if !re.MatchString(s) {
		return false
	}
	_, bad := reserved[s]
	return !bad
}

func Reserved(s string) bool {
	_, ok := reserved[s]
	return ok
}

// Validate returns a user-facing *rpcerr.Error on invalid/reserved names.
// kind ("kart"/"circuit"/"character") is interpolated into the message
// and data payload so downstream tooling can branch on it.
func Validate(kind, s string) error {
	if Reserved(s) {
		return rpcerr.UserError(rpcerr.TypeInvalidName,
			"%s name %q is reserved", kind, s).
			With("kind", kind).
			With("name", s)
	}
	if !re.MatchString(s) {
		return rpcerr.UserError(rpcerr.TypeInvalidName,
			"%s name %q is invalid (must match %s)", kind, s, Pattern).
			With("kind", kind).
			With("name", s).
			With("pattern", Pattern)
	}
	return nil
}

// ValidateAllowing behaves like Validate but treats each name in allow as
// permitted even if it would otherwise be Reserved. Useful for entity kinds
// whose reserved set is a strict subset of the package default (e.g. tunes
// must reject "none" but permit "default", which Validate forbids).
// Pattern validation still applies — the allow list short-circuits only
// the Reserved check.
func ValidateAllowing(kind, s string, allow ...string) error {
	for _, a := range allow {
		if s == a {
			if !re.MatchString(s) {
				return rpcerr.UserError(rpcerr.TypeInvalidName,
					"%s name %q is invalid (must match %s)", kind, s, Pattern).
					With("kind", kind).
					With("name", s).
					With("pattern", Pattern)
			}
			return nil
		}
	}
	return Validate(kind, s)
}

// SplitUserHost parses a "user@host[:port]" SSH target. host may include a
// colon+port; the caller forwards it verbatim. An empty target returns
// an error; a target without an @ returns (user="", host=target, nil).
func SplitUserHost(target string) (user, host string, err error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", "", errors.New("SSH target is required")
	}
	at := strings.LastIndex(target, "@")
	if at < 0 {
		return "", target, nil
	}
	user = target[:at]
	host = target[at+1:]
	if user == "" || host == "" {
		return "", "", fmt.Errorf("invalid SSH target %q: expected user@host", target)
	}
	return user, host, nil
}

// SplitHostPort breaks apart "host" or "host:port". Passing a bare host
// returns (host, "", nil). A literal IPv6 address should be wrapped in
// brackets (`[::1]:22`); bare IPv6 without a port is returned as-is.
func SplitHostPort(host string) (hostname, port string, err error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return "", "", errors.New("host is required")
	}
	// Bracketed IPv6: `[addr]` or `[addr]:port`.
	if strings.HasPrefix(host, "[") {
		end := strings.Index(host, "]")
		if end < 0 {
			return "", "", fmt.Errorf("invalid host %q: unterminated IPv6 bracket", host)
		}
		hostname = host[1:end]
		rest := host[end+1:]
		if rest == "" {
			return hostname, "", nil
		}
		if !strings.HasPrefix(rest, ":") {
			return "", "", fmt.Errorf("invalid host %q: expected :port after bracket", host)
		}
		return hostname, rest[1:], nil
	}
	// Ambiguous bare IPv6 (multiple colons) — no port possible.
	if strings.Count(host, ":") > 1 {
		return host, "", nil
	}
	if i := strings.IndexByte(host, ':'); i >= 0 {
		return host[:i], host[i+1:], nil
	}
	return host, "", nil
}

// SSHArgsFor builds the leading ssh argv for a "user@host[:port]" target:
// returns e.g. ["-p", "2222", "alice@host"] or just ["alice@host"]. Used by
// client.SSHTransportArgs so circuit-add can probe a raw destination
// before any drift.<name> alias is on disk.
func SSHArgsFor(target string) ([]string, error) {
	user, host, err := SplitUserHost(target)
	if err != nil {
		return nil, err
	}
	hostPart, port, err := SplitHostPort(host)
	if err != nil {
		return nil, err
	}
	dest := hostPart
	if user != "" {
		dest = user + "@" + hostPart
	}
	if port == "" {
		return []string{dest}, nil
	}
	return []string{"-p", port, dest}, nil
}
