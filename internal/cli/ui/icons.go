package ui

import "strings"

// Icon catalog. Drift assumes a Nerd Font is installed in the terminal;
// glyphs come from the Font Awesome (nf-fa-*) range of Nerd Fonts v3.x.
// Terminals without a Nerd Font render tofu — that is the documented
// trade-off of plan 16's "Nerd Font assumed" stance.
//
// Code points are written as Go Unicode escapes so the source survives
// editors that don't have a Nerd Font installed; the trailing comment
// names the nf-* short hand. Keep the catalog small — add glyphs as
// panels actually need them, don't preload speculatively.
const (
	// Status / lifecycle.
	IconRunning     = "" // nf-fa-play
	IconStopped     = "" // nf-fa-stop
	IconStale       = "" // nf-fa-warning
	IconUnreachable = "" // nf-fa-times
	IconStarting    = "" // nf-fa-circle_o_notch
	IconError       = "" // nf-fa-times
	IconSuccess     = "" // nf-fa-check
	IconInfo        = "" // nf-fa-info_circle
	IconWarning     = "" // nf-fa-warning

	// Markers / dots.
	IconBullet    = "" // nf-fa-circle
	IconDot       = "" // nf-fa-circle
	IconHollowDot = "" // nf-fa-circle_o
	IconArrow     = "" // nf-fa-arrow_right

	// Resource types — one per dashboard tab.
	IconCircuit   = "" // nf-fa-sitemap
	IconKart      = "" // nf-fa-cube
	IconChest     = "" // nf-fa-archive
	IconCharacter = "" // nf-fa-user
	IconTune      = "" // nf-fa-paint_brush
	IconPort      = "" // nf-fa-random
	IconLog       = "" // nf-fa-file_text_o
	IconSkill     = "" // nf-fa-star
	IconAI        = "" // nf-fa-robot
	IconStar      = "" // nf-fa-star

	// Lifecycle actions.
	IconRun      = "" // nf-fa-play
	IconStart    = "" // nf-fa-play
	IconStop     = "" // nf-fa-stop
	IconRestart  = "" // nf-fa-refresh
	IconRebuild  = "" // nf-fa-wrench
	IconRecreate = "" // nf-fa-recycle
	IconDelete   = "" // nf-fa-trash
	IconClone    = "" // nf-fa-copy
	IconConnect  = "" // nf-fa-external_link
	IconMigrate  = "" // nf-fa-retweet
	IconAdd      = "" // nf-fa-plus
	IconEdit     = "" // nf-fa-pencil
	IconSave     = "" // nf-fa-save
	IconFilter   = "" // nf-fa-filter
	IconSearch   = "" // nf-fa-search

	// Navigation.
	IconChevronRight = "" // nf-fa-chevron_right
	IconChevronLeft  = "" // nf-fa-chevron_left
	IconChevronDown  = "" // nf-fa-chevron_down
	IconChevronUp    = "" // nf-fa-chevron_up
	IconCaretDown    = "" // nf-fa-caret_down
	IconCaretRight   = "" // nf-fa-caret_right

	// Misc.
	IconHelp = "" // nf-fa-question_circle
	IconQuit = "" // nf-fa-power_off
	IconKey  = "" // nf-fa-key
)

// Icon returns the glyph string. Kept as a function for API stability;
// callers can also reference the constants directly. With the Nerd Font
// fallback removed (plan 16) Icon is a thin pass-through.
func Icon(s string) string { return s }

// Label pairs an icon with a label for menu entries, status lines, and
// row prefixes. A single space separates them so callers don't have to
// remember the spacing convention; if either side is empty the helper
// degrades to the non-empty side.
func Label(icon, label string) string {
	icon = strings.TrimSpace(icon)
	label = strings.TrimSpace(label)
	switch {
	case icon == "" && label == "":
		return ""
	case icon == "":
		return label
	case label == "":
		return icon
	}
	return icon + " " + label
}
