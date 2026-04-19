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
