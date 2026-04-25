package ui

import "charm.land/bubbles/v2/key"

// Keys is the shared registry of key.Bindings used across every drift
// interactive surface. Footers and help modals render from these so the
// strings shown to the user can never drift from the keys they bind.
var Keys = struct {
	Quit, ForceQuit        key.Binding
	Help                   key.Binding
	Filter                 key.Binding
	Refresh                key.Binding
	Up, Down, Left, Right  key.Binding
	Tab, ShiftTab          key.Binding
	Enter, Escape          key.Binding
	Tab1, Tab2, Tab3, Tab4 key.Binding
	Tab5, Tab6, Tab7, Tab8 key.Binding
	Palette                key.Binding
}{
	Quit:      key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
	ForceQuit: key.NewBinding(key.WithKeys("Q", "ctrl+c"), key.WithHelp("Q", "force quit")),
	Help:      key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
	Filter:    key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
	Refresh:   key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
	Up:        key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
	Down:      key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
	Left:      key.NewBinding(key.WithKeys("left", "h"), key.WithHelp("←/h", "left")),
	Right:     key.NewBinding(key.WithKeys("right", "l"), key.WithHelp("→/l", "right")),
	Tab:       key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next tab")),
	ShiftTab:  key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "prev tab")),
	Enter:     key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select")),
	Escape:    key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
	Tab1:      key.NewBinding(key.WithKeys("1"), key.WithHelp("1", "status")),
	Tab2:      key.NewBinding(key.WithKeys("2"), key.WithHelp("2", "karts")),
	Tab3:      key.NewBinding(key.WithKeys("3"), key.WithHelp("3", "circuits")),
	Tab4:      key.NewBinding(key.WithKeys("4"), key.WithHelp("4", "chest")),
	Tab5:      key.NewBinding(key.WithKeys("5"), key.WithHelp("5", "characters")),
	Tab6:      key.NewBinding(key.WithKeys("6"), key.WithHelp("6", "tunes")),
	Tab7:      key.NewBinding(key.WithKeys("7"), key.WithHelp("7", "ports")),
	Tab8:      key.NewBinding(key.WithKeys("8"), key.WithHelp("8", "logs")),
	Palette:   key.NewBinding(key.WithKeys(":"), key.WithHelp(":", "command")),
}
