package pat

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// slugRE mirrors internal/name.Pattern. Inlined here rather than imported
// to avoid the pat → name → rpcerr → wire → pat cycle (wire references
// the Pat type defined in this package).
var slugRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// Slugify turns a human label like "Test PAT" into "test-pat", suitable
// for use as a registry handle. Non-alphanumeric runs collapse to a
// single hyphen; leading/trailing hyphens are trimmed; the result must
// satisfy the shared name.Pattern (lowercase alphanumeric + hyphen,
// starts with a letter, ≤63 chars).
//
// Returns an error when the input has no slug-able content (empty or
// pure punctuation), or when the result starts with a digit — both
// cases mean the caller has to fall back to asking for an explicit slug.
func Slugify(s string) (string, error) {
	var b strings.Builder
	prevHyphen := true // suppress leading hyphen
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevHyphen = false
		default:
			if !prevHyphen {
				b.WriteByte('-')
				prevHyphen = true
			}
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if out == "" {
		return "", errors.New("empty slug")
	}
	if len(out) > 63 {
		out = strings.TrimRight(out[:63], "-")
	}
	if !slugRE.MatchString(out) {
		return "", fmt.Errorf("derived slug %q is invalid (must start with a-z, then alphanumeric or '-', up to 63 chars)", out)
	}
	return out, nil
}
