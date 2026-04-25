// Package dashboard implements drift's flagship TUI: a tabbed live view
// of every circuit, kart, and port forward. Bare `drift` on a TTY drops
// into this; an explicit `drift dashboard` subcommand opens it directly.
package dashboard

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

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

// Panel is the small interface every tab implements. The root model
// routes Update / View to the focused panel and renders chrome around
// the active panel's view. Panels never own the screen — they get a
// content area and report what they want drawn in it.
type Panel interface {
	Init() tea.Cmd
	Update(msg tea.Msg) (Panel, tea.Cmd)
	// View returns the panel's body content. The root model wraps it
	// with the tab bar and footer.
	View(width, height int) string
	// Title is shown at the top of the active panel.
	Title() string
}

// Options configures a dashboard run.
type Options struct {
	// InitialTab selects which panel is focused on launch.
	InitialTab Tab
	// CircuitFilter scopes data to one circuit when non-empty.
	CircuitFilter string
	// Theme drives panel and chrome rendering.
	Theme *ui.Theme
	// Demo flips the data source from live RPC to fixtures (--demo).
	Demo bool
	// DriftVersion is shown in the status banner.
	DriftVersion string
	// DataSource fetches the live data each panel needs. Demo mode
	// substitutes a fixture loader implementing the same interface.
	DataSource DataSource
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

// model is the root tea.Model. It owns the tab state and routes messages
// to the focused panel.
type model struct {
	o      Options
	t      *ui.Theme
	width  int
	height int

	tab    Tab
	panels [tabCount]Panel
}

func newModel(o Options) *model {
	m := &model{o: o, t: o.Theme, tab: o.InitialTab}
	m.panels[TabStatus] = newStatusPanel(o)
	m.panels[TabKarts] = newKartsPanel(o)
	m.panels[TabCircuits] = newCircuitsPanel(o)
	m.panels[TabChest] = newResourcePanel(o, "chest", "name backend used-by", o.DataSource.Chest)
	m.panels[TabCharacters] = newResourcePanel(o, "characters", "name git-name git-email used-by", o.DataSource.Characters)
	m.panels[TabTunes] = newResourcePanel(o, "tunes", "name image features used-by", o.DataSource.Tunes)
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
	case tea.KeyMsg:
		k := msg.String()
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
		// Numeric tab jump.
		if len(k) == 1 && k[0] >= '1' && k[0] <= '8' {
			m.tab = Tab(int(k[0] - '1'))
			return m, nil
		}
	case tickMsg:
		// Forward the tick to every panel so live views can refresh.
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
	// Route everything else to the focused panel.
	p, cmd := m.panels[m.tab].Update(msg)
	m.panels[m.tab] = p
	return m, cmd
}

func (m *model) View() tea.View {
	if m.width == 0 {
		// Fallback for the very first frame before WindowSizeMsg arrives.
		m.width, m.height = 100, 30
	}
	body := m.panels[m.tab].View(m.width, m.height-3)
	frame := lipgloss.JoinVertical(lipgloss.Left,
		m.tabBar(),
		body,
		m.footer(),
	)
	return ui.AltScreenView(frame)
}

func (m *model) tabBar() string {
	parts := make([]string, tabCount)
	for i := Tab(0); i < tabCount; i++ {
		label := fmt.Sprintf(" %d %s ", i+1, i.String())
		if i == m.tab {
			parts[i] = m.t.AccentStyle.Bold(true).Render("▸" + label + "◂")
		} else {
			parts[i] = m.t.DimStyle.Render(" " + label + " ")
		}
	}
	row := strings.Join(parts, "")
	rule := m.t.DimStyle.Render(strings.Repeat("─", m.width))
	return row + "\n" + rule
}

func (m *model) footer() string {
	keys := []string{
		"[tab] next",
		"[1-8] jump",
		"[r] refresh",
		"[?] help",
		"[q] quit",
	}
	hint := strings.Join(keys, "  ")
	return m.t.DimStyle.Render(hint)
}
