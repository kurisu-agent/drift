// Package name validates kart, circuit, and character identifiers. Tiny
// and dependency-free so both drift and lakitu can import it.
package name

import (
	"regexp"

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
