// Package dashboard implements drift's flagship TUI: a tabbed live view
// of every circuit, kart, and port forward. Bare `drift` on a TTY drops
// into this; an explicit `drift dashboard` subcommand opens it directly.
package dashboard

import (
	"context"
	"io"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/kurisu-agent/drift/internal/cli/ui"
)

// Tab is a numeric index into the dashboard's tab bar.
type Tab int

const (
	TabStatus Tab = iota
	TabKarts
	TabCircuits
	TabChest
	TabCharacters
	TabTunes
	TabPorts
	TabLogs
	tabCount
)

func (t Tab) String() string {
	return []string{"status", "karts", "circuits", "chest", "characters", "tunes", "ports", "logs"}[t]
}

// Panel is the small interface every tab implements. Panels never own
// the screen — they get a content area and a *ui.Theme and report the
// rendered body. The root model wraps it with the tab bar and footer.
type Panel interface {
	Init() tea.Cmd
	Update(msg tea.Msg) (Panel, tea.Cmd)
	View(width, height int) string
	Title() string
	// ShortHelp returns the panel's contextual key bindings, prepended
	// to the global ones (tab/quit/help) by the dashboard footer.
	ShortHelp() []key.Binding
}

// Options configures a dashboard run.
type Options struct {
	InitialTab     Tab
	CircuitFilter  string
	Theme          *ui.Theme
	Demo           bool
	DriftVersion   string
	DataSource     DataSource
	MotionDisabled bool // true skips the entrance animation; mirrors --no-motion / DRIFT_NO_MOTION.

	// InitialFilter pre-fills the active panel's filter input and
	// captures the filter-active visual scenario without needing a
	// pre-driven key sequence harness. Live runs leave this empty;
	// the eval-screens loop sets it for filter-* scenarios.
	InitialFilter string

	// AccentOverride re-tints the dashboard's brand accent at startup
	// (focus border, active tab, header, etc.). Hex like "#6B50FF".
	// Live use case: dashboard anchored to one circuit re-tints to
	// that circuit's Color. Empty leaves the default Charple accent.
	AccentOverride string

	// LogsFollowDefault seeds the logs panel's follow toggle. Live
	// runs default to follow=true; the eval-screens loop uses this
	// to capture the paused-vs-follow scenarios.
	LogsFollowDefault *bool
}

// DataSource is the small surface every panel calls into. Implementations
// live in the cmd/drift entry point (live RPC) and internal/demo (fixtures).
type DataSource interface {
	Status(ctx context.Context) (StatusSnapshot, error)
	Karts(ctx context.Context, circuit string) ([]KartRow, error)
	Circuits(ctx context.Context) ([]CircuitRow, error)
	Chest(ctx context.Context) ([]ResourceRow, error)
	Characters(ctx context.Context) ([]ResourceRow, error)
	Tunes(ctx context.Context) ([]ResourceRow, error)
	Ports(ctx context.Context) ([]PortRow, error)
}

// Run launches the dashboard against the given options. Blocks until the
// user quits or ctx is cancelled. Returns nil on a clean exit.
func Run(ctx context.Context, in io.Reader, out io.Writer, o Options) error {
	if o.Theme == nil {
		o.Theme = ui.NewTheme(out, false)
	}
	root := newModel(o)
	_, err := ui.RunProgram(root, ui.RunProgramOptions{
		Context: ctx,
		Input:   in,
		Output:  out,
	})
	return err
}

// model is the root tea.Model. It owns the tab state and routes
// messages to the focused panel; tab navigation, ticker scheduling,
// and chrome live here.
type model struct {
	o      Options
	t      *ui.Theme
	help   help.Model
	width  int
	height int

	tab    Tab
	panels [tabCount]Panel
}

func newModel(o Options) *model {
	if o.AccentOverride != "" && o.Theme != nil {
		o.Theme = o.Theme.WithAccent(lipgloss.Color(o.AccentOverride))
	}
	hp := help.New()
	hp.Styles = helpStylesFor(o.Theme)
	m := &model{o: o, t: o.Theme, tab: o.InitialTab, help: hp}
	m.panels[TabStatus] = newStatusPanel(o)
	m.panels[TabKarts] = newKartsPanel(o)
	m.panels[TabCircuits] = newCircuitsPanel(o)
	m.panels[TabChest] = newResourcePanel(o, "chest", o.DataSource.Chest)
	m.panels[TabCharacters] = newResourcePanel(o, "characters", o.DataSource.Characters)
	m.panels[TabTunes] = newResourcePanel(o, "tunes", o.DataSource.Tunes)
	m.panels[TabPorts] = newPortsPanel(o)
	m.panels[TabLogs] = newLogsPanel(o)
	return m
}

func (m *model) Init() tea.Cmd {
	cmds := make([]tea.Cmd, 0, tabCount+1)
	for i := range m.panels {
		if c := m.panels[i].Init(); c != nil {
			cmds = append(cmds, c)
		}
	}
	cmds = append(cmds, tickCmd())
	return tea.Batch(cmds...)
}

type tickMsg time.Time

func tickCmd() tea.Cmd {
	return tea.Tick(10*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, ui.Keys.Quit, ui.Keys.ForceQuit):
			return m, tea.Quit
		case key.Matches(msg, ui.Keys.Tab):
			m.tab = (m.tab + 1) % tabCount
			return m, nil
		case key.Matches(msg, ui.Keys.ShiftTab):
			m.tab = (m.tab + tabCount - 1) % tabCount
			return m, nil
		}
		s := msg.String()
		if len(s) == 1 && s[0] >= '1' && s[0] <= '8' {
			m.tab = Tab(int(s[0] - '1'))
			return m, nil
		}
	case tickMsg:
		var cmds []tea.Cmd
		for i := range m.panels {
			p, c := m.panels[i].Update(msg)
			m.panels[i] = p
			if c != nil {
				cmds = append(cmds, c)
			}
		}
		cmds = append(cmds, tickCmd())
		return m, tea.Batch(cmds...)
	}
	p, cmd := m.panels[m.tab].Update(msg)
	m.panels[m.tab] = p
	return m, cmd
}

func (m *model) View() tea.View {
	if m.width == 0 {
		m.width, m.height = 100, 30
	}
	// Outer rounded border eats 2 cols + 2 rows; horizontal padding 1
	// each side eats another 2 cols. The inner width feeding the tab
	// strip and the active panel is the remainder.
	innerW := m.width - 4
	if innerW < 1 {
		innerW = 1
	}

	bar := m.renderTabBar(innerW)
	footer := m.renderFooter(innerW)
	bodyH := m.height - 2 - lipgloss.Height(bar) - lipgloss.Height(footer)
	if bodyH < 1 {
		bodyH = 1
	}

	body := m.panels[m.tab].View(innerW, bodyH)
	body = lipgloss.NewStyle().Width(innerW).Height(bodyH).Render(body)

	inner := lipgloss.JoinVertical(lipgloss.Left, bar, body, footer)

	outer := outerBorderStyle(m.t).Render(inner)
	return ui.AltScreenView(outer)
}

// outerBorderStyle is the rounded chrome that wraps the whole dashboard.
// theme.Border.Subtle is the brand-guideline weight; padding 0/1 keeps
// content off the rule.
func outerBorderStyle(t *ui.Theme) lipgloss.Style {
	st := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Padding(0, 1)
	if t != nil && t.Enabled {
		st = st.BorderForeground(t.Border.Subtle.GetForeground())
	}
	return st
}

// renderTabBar lays out the eight tab labels as a single row inside the
// outer chrome. Per plan-16 brand guidelines, active vs inactive is
// communicated by color only (theme.Border.Focus vs theme.Text.Muted) —
// no bg, no underline, no padding swap. The horizontal rule beneath has
// a gap under the active tab so the active label welds into the body
// region instead of sitting under a closed line.
func (m *model) renderTabBar(width int) string {
	accent, muted, subtle := m.tabStyles()

	cellPad := strings.Repeat(" ", 2)
	sep := " · "
	sepW := lipgloss.Width(sep)

	parts := make([]string, 0, 2*tabCount-1)
	cursor := 0
	activeStart, activeWidth := 0, 0
	for i := Tab(0); i < tabCount; i++ {
		label := cellPad + i.String() + cellPad
		cellW := lipgloss.Width(label)
		switch {
		case i == m.tab:
			parts = append(parts, accent.Render(label))
			activeStart = cursor
			activeWidth = cellW
		default:
			parts = append(parts, muted.Render(label))
		}
		cursor += cellW
		if i < tabCount-1 {
			parts = append(parts, subtle.Render(sep))
			cursor += sepW
		}
	}
	row := lipgloss.JoinHorizontal(lipgloss.Top, parts...)

	runes := []rune(strings.Repeat("─", width))
	for i := activeStart; i < activeStart+activeWidth && i < len(runes); i++ {
		runes[i] = ' '
	}
	rule := subtle.Render(string(runes))
	return lipgloss.JoinVertical(lipgloss.Left, row, rule)
}

// tabStyles returns the three foregrounds the tab strip composes
// against (active accent, inactive muted, subtle separator/rule).
// Falls back to identity styles when the theme is disabled.
func (m *model) tabStyles() (accent, muted, subtle lipgloss.Style) {
	if m.t == nil || !m.t.Enabled {
		return lipgloss.NewStyle(), lipgloss.NewStyle(), lipgloss.NewStyle()
	}
	return m.t.Border.Focus, m.t.MutedStyle, m.t.Border.Subtle
}

// renderFooter delegates to bubbles/v2/help so footer chrome stays in
// sync with the actual key bindings. Active panel contributes its own
// bindings ahead of the global ones.
func (m *model) renderFooter(width int) string {
	hk := keyMapFor(m.panels[m.tab])
	m.help.SetWidth(width)
	return m.help.View(hk)
}

// helpStylesFor matches help.Model's styles to the theme. Disabled
// themes (JSON / NO_COLOR / non-TTY) get identity styles so output
// stays ANSI-free.
func helpStylesFor(t *ui.Theme) help.Styles {
	if t == nil || !t.Enabled {
		return help.Styles{
			Ellipsis:       lipgloss.NewStyle(),
			ShortKey:       lipgloss.NewStyle(),
			ShortDesc:      lipgloss.NewStyle(),
			ShortSeparator: lipgloss.NewStyle(),
			FullKey:        lipgloss.NewStyle(),
			FullDesc:       lipgloss.NewStyle(),
			FullSeparator:  lipgloss.NewStyle(),
		}
	}
	if t.Dark {
		return help.DefaultDarkStyles()
	}
	return help.DefaultLightStyles()
}

// keyMap implements help.KeyMap by interleaving the active panel's
// bindings with the dashboard's globals.
type keyMap struct {
	panel []key.Binding
}

func keyMapFor(p Panel) keyMap {
	return keyMap{panel: p.ShortHelp()}
}

func (k keyMap) ShortHelp() []key.Binding {
	out := append([]key.Binding{}, k.panel...)
	out = append(out, ui.Keys.Tab, ui.Keys.Refresh, ui.Keys.Help, ui.Keys.Quit)
	return out
}

func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		k.panel,
		{ui.Keys.Tab, ui.Keys.ShiftTab, ui.Keys.Tab1, ui.Keys.Tab2, ui.Keys.Tab3, ui.Keys.Tab4, ui.Keys.Tab5, ui.Keys.Tab6, ui.Keys.Tab7, ui.Keys.Tab8},
		{ui.Keys.Refresh, ui.Keys.Filter, ui.Keys.Help, ui.Keys.Quit, ui.Keys.ForceQuit},
	}
}
