// Package name validates kart, circuit, and character identifiers used across
// drift and lakitu. The same regex and reserved-word list applies to every
// name in the system.
//
// This package is deliberately tiny and dependency-free so both the drift
// client and the lakitu server can import it without pulling in CLI or RPC
// machinery.
package name

import (
	"regexp"

	"github.com/kurisu-agent/drift/internal/rpcerr"
)

// Pattern is the canonical regex for drift identifiers: lowercase
// alphanumeric + hyphen, 1–63 chars, starting with a letter.
const Pattern = `^[a-z][a-z0-9-]{0,62}$`

var re = regexp.MustCompile(Pattern)

// Reserved names cannot be used for karts, circuits, or characters — they
// collide with the tune sentinel values.
var reserved = map[string]struct{}{
	"default": {},
	"none":    {},
}

// Valid reports whether s is a syntactically valid, non-reserved name.
func Valid(s string) bool {
	if !re.MatchString(s) {
		return false
	}
	_, bad := reserved[s]
	return !bad
}

// Reserved reports whether s matches one of the reserved identifiers.
func Reserved(s string) bool {
	_, ok := reserved[s]
	return ok
}

// Validate returns a user-facing *rpcerr.Error when s is not a valid name.
// kind is a short noun ("kart", "circuit", "character") interpolated into
// the error message and the data payload so downstream tooling can branch
// on it.
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
