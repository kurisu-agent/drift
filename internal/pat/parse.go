package pat

import (
	"regexp"
	"strings"
	"time"
)

// ParsedPaste is the result of running Parse on a settings-page paste
// body. Missing anchors leave fields empty rather than erroring; the
// parser is forgiving on purpose so a future GitHub UI reflow degrades
// to "fewer fields auto-filled" instead of a hard failure.
type ParsedPaste struct {
	Name        string
	Description string
	Owner       string
	ExpiresAt   string // YYYY-MM-DD
	CreatedAt   string // YYYY-MM-DD
	Repos       []string
	ReposAll    bool // "This token has access to all repositories owned by you."
	Perms       []string
	UserPerms   []string
}

// Parse extracts what it can from a github settings-page paste body.
// today is supplied so "Created today." resolves to a stable date in
// tests; production callers pass time.Now().
//
// The parser walks the body line by line, tracking which section it's
// in. Section headers act as anchors: "Repository access", "User
// permissions", "Repository permissions". Anything outside a known
// section (the page chrome, the footer) is ignored.
func Parse(body string, today time.Time) ParsedPaste {
	var p ParsedPaste
	lines := strings.Split(body, "\n")

	titleIdx := findTitleIndex(lines)
	if titleIdx >= 0 {
		p.Name = strings.TrimSpace(lines[titleIdx])
	}
	if d, ok := findDescription(lines, titleIdx); ok {
		p.Description = d
	}

	type section int
	const (
		secNone section = iota
		secRepoAccess
		secUserPerms
		secRepoPerms
	)
	cur := secNone

	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}

		// Section transitions.
		switch line {
		case "Repository access":
			cur = secRepoAccess
			continue
		case "User permissions":
			cur = secUserPerms
			continue
		case "Repository permissions":
			cur = secRepoPerms
			continue
		case "Footer", "Footer navigation":
			cur = secNone
			continue
		}

		// Top-of-page anchors apply regardless of section.
		if v, ok := matchExpires(line); ok {
			p.ExpiresAt = v
			continue
		}
		if v, ok := matchCreated(line, today); ok {
			p.CreatedAt = v
			continue
		}
		if v, ok := matchAccessOn(line); ok {
			p.Owner = v
			continue
		}

		switch cur {
		case secRepoAccess:
			switch {
			case isRepoLine(line):
				p.Repos = append(p.Repos, line)
			case isAllReposSentinel(line):
				p.ReposAll = true
			}
		case secRepoPerms:
			if line == "This token does not have any repository permissions." {
				continue
			}
			p.Perms = append(p.Perms, explodePerm(line)...)
		case secUserPerms:
			if line == "This token does not have any user permissions." {
				continue
			}
			p.UserPerms = append(p.UserPerms, explodePerm(line)...)
		}
	}

	return p
}

// findTitleIndex returns the index of the line that should be treated
// as the PAT's display name. The settings page renders the name as the
// first heading after a "Skip to content" navlink; we tolerate either
// it being present or absent.
func findTitleIndex(lines []string) int {
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" || t == "Skip to content" {
			continue
		}
		return i
	}
	return -1
}

// findDescription returns the paragraph between the title line and the
// first known anchor (Created/Expires/Access on/Repository access). The
// literal "No description" surfaces as empty so callers don't have to
// special-case it.
func findDescription(lines []string, titleIdx int) (string, bool) {
	if titleIdx < 0 {
		return "", false
	}
	var collected []string
	seenBlank := false
	for i := titleIdx + 1; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if t == "" {
			if len(collected) > 0 {
				break
			}
			seenBlank = true
			continue
		}
		if isAnchorLine(t) {
			break
		}
		collected = append(collected, t)
		_ = seenBlank
	}
	desc := strings.TrimSpace(strings.Join(collected, " "))
	if desc == "" || desc == "No description" {
		return "", true
	}
	return desc, true
}

func isAnchorLine(line string) bool {
	if strings.HasPrefix(line, "Created on ") || strings.HasPrefix(line, "Created today") {
		return true
	}
	if strings.HasPrefix(line, "Expires on ") {
		return true
	}
	if strings.HasPrefix(line, "Access on @") {
		return true
	}
	switch line {
	case "Repository access", "User permissions", "Repository permissions", "Footer":
		return true
	}
	return false
}

// expiresRe matches "Expires on Tue, Jul 28 2026" or the same with a
// trailing period. We tolerate the trailing "." so a paste from a
// future GitHub copy variant still resolves.
var expiresRe = regexp.MustCompile(`^Expires on\s+(?:[A-Za-z]{3},\s+)?([A-Za-z]{3})\s+(\d{1,2})\s+(\d{4})\.?\s*$`)

func matchExpires(line string) (string, bool) {
	m := expiresRe.FindStringSubmatch(line)
	if m == nil {
		return "", false
	}
	return ymdFromMonDay(m[1], m[2], m[3])
}

// createdOnRe handles "Created on Mon, Apr 27 2026" or "Created on Apr 27 2026".
var createdOnRe = regexp.MustCompile(`^Created on\s+(?:[A-Za-z]{3},\s+)?([A-Za-z]{3})\s+(\d{1,2})\s+(\d{4})\.?\s*$`)

func matchCreated(line string, today time.Time) (string, bool) {
	if strings.HasPrefix(line, "Created today") {
		return today.Format("2006-01-02"), true
	}
	m := createdOnRe.FindStringSubmatch(line)
	if m == nil {
		return "", false
	}
	return ymdFromMonDay(m[1], m[2], m[3])
}

// accessOnRe matches "Access on @<login> <login>" for user-owned PATs
// and "Access on the @<login> <login> organization" for org-owned ones.
// The GitHub UI prints the owner twice (once as a mention, once as a
// plaintext name); we take the first @-prefixed token.
var accessOnRe = regexp.MustCompile(`^Access on\s+(?:the\s+)?@([A-Za-z0-9][A-Za-z0-9-]*)\b`)

func matchAccessOn(line string) (string, bool) {
	m := accessOnRe.FindStringSubmatch(line)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// repoLineRe gates the Repository-access block: only `<owner>/<repo>`
// shaped lines are captured. This keeps "All repositories" or future
// summary lines from sneaking in as bogus repo entries.
var repoLineRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9._-]+$`)

func isRepoLine(line string) bool {
	return repoLineRe.MatchString(line)
}

// isAllReposSentinel matches the "all repositories" paragraph GitHub
// renders when the PAT was scoped to "All repositories" rather than a
// specific list. Phrasing has shifted across UI revisions ("owned by
// you" vs "you own"), so we match on the substring rather than the
// exact line.
func isAllReposSentinel(line string) bool {
	low := strings.ToLower(line)
	return strings.Contains(low, "access to all repositories")
}

// explodePerm turns one GitHub permissions line into one entry per
// resource. "Read access to issues and metadata" ⇒
// ["issues: read", "metadata: read"]. "Read and Write access to actions,
// code, and workflows" ⇒ ["actions: write", "code: write",
// "workflows: write"]; Oxford commas in the GitHub copy are stripped so
// "code," doesn't leak into the resource name. Anything that doesn't
// match the "<level> access to …" template is preserved verbatim so
// unknown shapes survive instead of being dropped.
func explodePerm(line string) []string {
	level, rest, ok := splitPermLevel(line)
	if !ok {
		return []string{line}
	}
	resources := splitOnAnd(rest)
	out := make([]string, 0, len(resources))
	for _, r := range resources {
		out = append(out, r+": "+level)
	}
	return out
}

func splitPermLevel(line string) (level, rest string, ok bool) {
	const suffix = " access to "
	idx := strings.Index(line, suffix)
	if idx <= 0 {
		return "", "", false
	}
	prefix := line[:idx]
	rest = strings.TrimSpace(line[idx+len(suffix):])
	switch strings.ToLower(prefix) {
	case "read":
		return "read", rest, true
	case "write":
		return "write", rest, true
	case "admin":
		return "admin", rest, true
	case "read and write":
		return "write", rest, true
	}
	return "", "", false
}

// splitOnAnd splits "actions, code, and workflows" into ["actions",
// "code", "workflows"]. The Oxford comma in GitHub's copy leaves a
// trailing comma on the second-to-last piece after the " and " split
// ("actions, code," / "workflows"), so we strip stray commas after
// trimming whitespace.
func splitOnAnd(s string) []string {
	parts := strings.Split(s, " and ")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		for _, q := range strings.Split(p, ", ") {
			q = strings.Trim(strings.TrimSpace(q), ",")
			q = strings.TrimSpace(q)
			if q != "" {
				out = append(out, q)
			}
		}
	}
	return out
}

var monthIndex = map[string]string{
	"Jan": "01", "Feb": "02", "Mar": "03", "Apr": "04",
	"May": "05", "Jun": "06", "Jul": "07", "Aug": "08",
	"Sep": "09", "Oct": "10", "Nov": "11", "Dec": "12",
}

func ymdFromMonDay(mon, day, year string) (string, bool) {
	m, ok := monthIndex[mon]
	if !ok {
		return "", false
	}
	d := day
	if len(d) == 1 {
		d = "0" + d
	}
	return year + "-" + m + "-" + d, true
}
