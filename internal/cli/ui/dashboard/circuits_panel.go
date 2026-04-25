package dashboard

import (
	"context"
	"fmt"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
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
	cols := []table.Column{
		{Title: "name", Width: 14},
		{Title: "host", Width: 28},
		{Title: "default", Width: 8},
		{Title: "lakitu", Width: 10},
		{Title: "latency", Width: 10},
		{Title: "state", Width: 12},
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
		state := "unreachable"
		if r.Reachable {
			state = "reachable"
		}
		def := "—"
		if r.Default {
			def = "*"
		}
		latency := "—"
		if r.LatencyMS > 0 {
			latency = fmt.Sprintf("%dms", r.LatencyMS)
		}
		styledState := state
		if t != nil && t.Enabled {
			if r.Reachable {
				styledState = t.SuccessStyle.Render(state)
			} else {
				styledState = t.ErrorStyle.Render(state)
			}
		}
		out[i] = table.Row{r.Name, r.Host, def, dashIfEmpty(r.Lakitu), latency, styledState}
	}
	return out
}
