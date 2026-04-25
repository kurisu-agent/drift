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
	InitialTab    Tab
	CircuitFilter string
	Theme         *ui.Theme
	Demo          bool
	DriftVersion  string
	DataSource    DataSource
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
	bar := m.renderTabBar()
	footer := m.renderFooter()
	bodyHeight := m.height - lipgloss.Height(bar) - lipgloss.Height(footer)
	if bodyHeight < 1 {
		bodyHeight = 1
	}
	body := m.panels[m.tab].View(m.width, bodyHeight)
	body = lipgloss.NewStyle().Width(m.width).Height(bodyHeight).Render(body)
	frame := lipgloss.JoinVertical(lipgloss.Left, bar, body, footer)
	return ui.AltScreenView(frame)
}

// renderTabBar lays out the eight tab labels as a single row, with the
// active tab styled accent + underlined and the rest dim. A horizontal
// rule below separates the bar from the body.
func (m *model) renderTabBar() string {
	dim := dimFor(m.t)
	bold := boldFor(m.t)
	active := bold.Underline(true).Padding(0, 2)
	if m.t != nil && m.t.Enabled {
		active = m.t.AccentStyle.Bold(true).Underline(true).Padding(0, 2)
	}
	inactive := dim.Padding(0, 2)

	separator := dim.Render("·")
	parts := make([]string, 0, 2*tabCount-1)
	for i := Tab(0); i < tabCount; i++ {
		st := inactive
		if i == m.tab {
			st = active
		}
		parts = append(parts, st.Render(i.String()))
		if i < tabCount-1 {
			parts = append(parts, separator)
		}
	}
	row := lipgloss.JoinHorizontal(lipgloss.Top, parts...)
	rule := dim.Render(strings.Repeat("─", m.width))
	return lipgloss.JoinVertical(lipgloss.Left, row, rule)
}

// renderFooter delegates to bubbles/v2/help so footer chrome stays in
// sync with the actual key bindings. Active panel contributes its own
// bindings ahead of the global ones.
func (m *model) renderFooter() string {
	hk := keyMapFor(m.panels[m.tab])
	m.help.SetWidth(m.width)
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
