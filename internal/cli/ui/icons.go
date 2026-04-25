package ui

// Icon registry. Drift uses unicode glyphs (BMP, no nerd-font dependency)
// as primary; DRIFT_NO_NERDFONT degrades to plain ASCII so call sites
// stay grep-friendly. Callers go through Icon() rather than embedding
// raw runes — keeps the catalog editable in one place when designs shift.

const (
	IconRunning      rune = '▶'
	IconStopped      rune = '■'
	IconStale        rune = '⚠'
	IconUnreachable  rune = '✕'
	IconStarting     rune = '◐'
	IconError        rune = '✘'
	IconSuccess      rune = '✓'
	IconInfo         rune = 'ℹ'
	IconWarning      rune = '⚠'
	IconBullet       rune = '•'
	IconDot          rune = '●'
	IconHollowDot    rune = '○'
	IconArrow        rune = '▸'
	IconCircuit      rune = '◆'
	IconKart         rune = '◼'
	IconChest        rune = '◈'
	IconCharacter    rune = '◇'
	IconTune         rune = '◉'
	IconPort         rune = '⇄'
	IconLog          rune = '≡'
	IconSkill        rune = '★'
	IconRun          rune = '▶'
	IconAI           rune = '✦'
	IconStart        rune = '▶'
	IconStop         rune = '■'
	IconRestart      rune = '↻'
	IconRebuild      rune = '⚒'
	IconRecreate     rune = '↺'
	IconDelete       rune = '✗'
	IconClone        rune = '⎘'
	IconConnect      rune = '↪'
	IconMigrate      rune = '↔'
	IconAdd          rune = '+'
	IconEdit         rune = '✎'
	IconSave         rune = '⤓'
	IconFilter       rune = '⍷'
	IconSearch       rune = '⌕'
	IconChevronRight rune = '❯'
	IconChevronLeft  rune = '❮'
	IconStar         rune = '★'
	IconCaretDown    rune = '⌄'
	IconCaretRight   rune = '›'
	IconHelp         rune = '?'
	IconQuit         rune = '⏻'
)

// Icon returns the unicode glyph for r, or its ASCII fallback when
// DRIFT_NO_NERDFONT is set.
func Icon(r rune) string {
	if NerdFont() {
		return string(r)
	}
	if fb, ok := asciiFallback[r]; ok {
		return fb
	}
	return string(r)
}

var asciiFallback = map[rune]string{
	IconRunning:      ">",
	IconStopped:      "#",
	IconStale:        "!",
	IconUnreachable:  "x",
	IconStarting:     "*",
	IconError:        "x",
	IconSuccess:      "ok",
	IconInfo:         "i",
	IconBullet:       "*",
	IconDot:          "*",
	IconHollowDot:    "o",
	IconArrow:        ">",
	IconCircuit:      "C",
	IconKart:         "K",
	IconChest:        "$",
	IconCharacter:    "U",
	IconTune:         "T",
	IconPort:         "P",
	IconLog:          "L",
	IconSkill:        "*",
	IconAI:           "A",
	IconRestart:      "r",
	IconRebuild:      "b",
	IconRecreate:     "R",
	IconDelete:       "x",
	IconClone:        "+",
	IconConnect:      ">",
	IconMigrate:      "<>",
	IconEdit:         "/",
	IconSave:         "s",
	IconFilter:       "/",
	IconSearch:       "?",
	IconChevronRight: ">",
	IconChevronLeft:  "<",
	IconCaretDown:    "v",
	IconCaretRight:   ">",
	IconQuit:         "q",
}
