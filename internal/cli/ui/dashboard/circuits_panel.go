package dashboard

import (
	"context"
	"fmt"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/kurisu-agent/drift/internal/cli/ui"
)

type circuitsPanel struct {
	o     Options
	t     *ui.Theme
	tbl   table.Model
	rows  []CircuitRow
	err   string
	ready bool
}

func newCircuitsPanel(o Options) Panel {
	// Leading "·" column reserves two cells for the per-circuit color
	// swatch + a star marking the default circuit. Width 4 fits the
	// swatch + space + star without crowding the name column.
	cols := []table.Column{
		{Title: "·", Width: 4},
		{Title: "name", Width: 14},
		{Title: "host", Width: 28},
		{Title: "lakitu", Width: 10},
		{Title: "latency", Width: 10},
		{Title: "state", Width: 14},
	}
	tbl := table.New(table.WithColumns(cols), table.WithFocused(true))
	tbl.SetStyles(tableStyles(o.Theme))
	return &circuitsPanel{o: o, t: o.Theme, tbl: tbl}
}

func (p *circuitsPanel) Title() string { return "circuits" }
func (p *circuitsPanel) ShortHelp() []key.Binding {
	return []key.Binding{ui.Keys.Up, ui.Keys.Down}
}

func (p *circuitsPanel) Init() tea.Cmd { return p.refreshCmd() }

type circuitsLoadedMsg struct{ rows []CircuitRow }
type circuitsErrMsg struct{ err string }

func (p *circuitsPanel) refreshCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		rows, err := p.o.DataSource.Circuits(ctx)
		if err != nil {
			return circuitsErrMsg{err: err.Error()}
		}
		return circuitsLoadedMsg{rows: rows}
	}
}

func (p *circuitsPanel) Update(msg tea.Msg) (Panel, tea.Cmd) {
	switch m := msg.(type) {
	case circuitsLoadedMsg:
		p.rows = m.rows
		p.tbl.SetRows(toCircuitTableRows(m.rows, p.t))
		p.err = ""
		p.ready = true
		return p, nil
	case circuitsErrMsg:
		p.err = m.err
		return p, nil
	case tickMsg:
		return p, p.refreshCmd()
	case tea.KeyPressMsg:
		if m.String() == "r" {
			return p, p.refreshCmd()
		}
	}
	var cmd tea.Cmd
	p.tbl, cmd = p.tbl.Update(msg)
	return p, cmd
}

func (p *circuitsPanel) View(width, height int) string {
	if p.err != "" {
		return panelError(p.t, p.err, width, height)
	}
	if !p.ready {
		return panelEmpty(p.t, "loading circuits...", width, height)
	}
	if len(p.rows) == 0 {
		return panelEmpty(p.t, "no circuits configured. try `drift circuit add user@host`.", width, height)
	}
	p.tbl.SetWidth(width)
	p.tbl.SetHeight(maxInt(3, height-2))
	return p.tbl.View()
}

func toCircuitTableRows(rs []CircuitRow, t *ui.Theme) []table.Row {
	out := make([]table.Row, len(rs))
	for i, r := range rs {
		latency := "—"
		if r.LatencyMS > 0 {
			latency = fmt.Sprintf("%dms", r.LatencyMS)
		}
		out[i] = table.Row{
			circuitMarker(r, t),
			r.Name,
			r.Host,
			dashIfEmpty(r.Lakitu),
			latency,
			circuitState(r, t),
		}
	}
	return out
}

// circuitMarker renders the leading "swatch + default-star" cell. The
// swatch is a single block in the per-circuit Color (or a muted block
// when none is set); the star marks the default circuit. Both fit in
// 3 cells; the column reserves 4 for breathing room.
func circuitMarker(r CircuitRow, t *ui.Theme) string {
	const block = "▮"
	swatch := block
	star := " "
	if r.Default {
		star = ui.IconStar
	}
	if t == nil || !t.Enabled {
		return swatch + " " + star
	}
	swatchStyle := lipgloss.NewStyle().Foreground(t.Border.Subtle.GetForeground())
	if r.Color != "" {
		swatchStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(r.Color))
	}
	starStyle := lipgloss.NewStyle().Foreground(t.AccentColor)
	return swatchStyle.Render(swatch) + " " + starStyle.Render(star)
}

// circuitState renders the reachable/unreachable column as a status
// pill so the column reads at a glance. Reachable → success.Pill,
// unreachable → error.Pill.
func circuitState(r CircuitRow, t *ui.Theme) string {
	state := "unreachable"
	if r.Reachable {
		state = "reachable"
	}
	if t == nil || !t.Enabled {
		return state
	}
	if r.Reachable {
		return t.Status.Success.Pill.Render(state)
	}
	return t.Status.Error.Pill.Render(state)
}
