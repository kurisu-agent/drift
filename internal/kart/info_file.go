package kart

import (
	"os"
	"strings"

	"github.com/kurisu-agent/drift/internal/icons"
)

// HostTimezone returns the host's timezone as an IANA name (e.g.
// "Europe/Berlin"), falling back to "UTC" if the system doesn't expose
// it the standard way. Used to seed the {{ .Timezone }} template var so
// the in-kart zellij clock matches the operator's wall-clock without
// per-kart configuration.
func HostTimezone() string {
	if tz, _ := os.ReadFile("/etc/timezone"); len(tz) > 0 {
		return strings.TrimSpace(string(tz))
	}
	if link, err := os.Readlink("/etc/localtime"); err == nil {
		// /etc/localtime → /usr/share/zoneinfo/<Area>/<Loc>
		const root = "zoneinfo/"
		if i := strings.Index(link, root); i >= 0 {
			return link[i+len(root):]
		}
	}
	return "UTC"
}

// tuneIcon / tuneColor pull the display defaults out of the tune. Both
// safe on nil — empty defaults to "no icon" / "overlay grey" downstream.
//
// tuneIcon resolves nerd-font catalog names (e.g. `dev-go`,
// `cod-rocket`) to the corresponding glyph; literal glyphs and emoji
// pass through unchanged via icons.Resolve. Letting users write a name
// in the tune YAML is far friendlier than asking them to paste the
// raw codepoint, and the catalog (10k+ entries) covers everything the
// in-kart UI is ever likely to want to render.
func tuneIcon(t *Tune) string {
	if t == nil {
		return ""
	}
	return icons.Resolve(t.Icon)
}
func tuneColor(t *Tune) string {
	if t == nil {
		return ""
	}
	return t.Color
}

// characterIcon resolves the character's Icon to a rendered glyph
// (catalog name → codepoint via icons.Resolve; literal glyph or emoji
// passes through). Empty when the character has no icon set or no
// character is selected.
func characterIcon(c *Character) string {
	if c == nil {
		return ""
	}
	return icons.Resolve(c.Icon)
}

// characterColor returns the character's Color verbatim (catppuccin
// palette name); empty when unset.
func characterColor(c *Character) string {
	if c == nil {
		return ""
	}
	return c.Color
}

// characterDisplayName returns the character's DisplayName, falling
// back to the character's YAML key (the safe ASCII identifier) when
// no display_name is set so the topbar always has *something* to
// show. Empty only when no character is selected at all.
func characterDisplayName(c *Character, fallback string) string {
	if c == nil {
		return ""
	}
	if c.DisplayName != "" {
		return c.DisplayName
	}
	return fallback
}
