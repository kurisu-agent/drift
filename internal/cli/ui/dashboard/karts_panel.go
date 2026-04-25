package dashboard

import (
	"context"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"github.com/kurisu-agent/drift/internal/cli/ui"
)

type kartsPanel struct {
	o     Options
	t     *ui.Theme
	tbl   table.Model
	rows  []KartRow
	err   string
	ready bool
}

func newKartsPanel(o Options) Panel {
	cols := []table.Column{
		{Title: "circuit", Width: 12},
		{Title: "name", Width: 20},
		{Title: "status", Width: 10},
		{Title: "tune", Width: 12},
		{Title: "source", Width: 10},
		{Title: "autostart", Width: 9},
	}
	tbl := table.New(table.WithColumns(cols), table.WithFocused(true))
	tbl.SetStyles(tableStyles(o.Theme))
	return &kartsPanel{o: o, t: o.Theme, tbl: tbl}
}

func (p *kartsPanel) Title() string { return "karts" }

func (p *kartsPanel) ShortHelp() []key.Binding {
	return []key.Binding{ui.Keys.Up, ui.Keys.Down}
}

func (p *kartsPanel) Init() tea.Cmd { return p.refreshCmd() }

type kartsLoadedMsg struct{ rows []KartRow }
type kartsErrMsg struct{ err string }

func (p *kartsPanel) refreshCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		rows, err := p.o.DataSource.Karts(ctx, p.o.CircuitFilter)
		if err != nil {
			return kartsErrMsg{err: err.Error()}
		}
		return kartsLoadedMsg{rows: rows}
	}
}

func (p *kartsPanel) Update(msg tea.Msg) (Panel, tea.Cmd) {
	switch m := msg.(type) {
	case kartsLoadedMsg:
		p.rows = m.rows
		p.tbl.SetRows(toKartTableRows(m.rows, p.t))
		p.err = ""
		p.ready = true
		return p, nil
	case kartsErrMsg:
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

func (p *kartsPanel) View(width, height int) string {
	if p.err != "" {
		return panelError(p.t, p.err, width, height)
	}
	if !p.ready {
		return panelEmpty(p.t, "loading karts...", width, height)
	}
	if len(p.rows) == 0 {
		return panelEmpty(p.t, "no karts on any configured circuit", width, height)
	}
	p.tbl.SetWidth(width)
	p.tbl.SetHeight(maxInt(3, height-2))
	return p.tbl.View()
}

func toKartTableRows(rs []KartRow, t *ui.Theme) []table.Row {
	out := make([]table.Row, len(rs))
	for i, r := range rs {
		auto := "—"
		if r.Autostart {
			auto = "yes"
		}
		out[i] = table.Row{
			r.Circuit,
			r.Name,
			styleStatus(t, r.Status),
			dashIfEmpty(r.Tune),
			dashIfEmpty(r.Source),
			auto,
		}
	}
	return out
}

func styleStatus(t *ui.Theme, status string) string {
	if t == nil || !t.Enabled {
		return status
	}
	switch status {
	case "running":
		return t.SuccessStyle.Render(status)
	case "stopped":
		return t.DimStyle.Render(status)
	case "stale":
		return t.WarnStyle.Render(status)
	case "error", "unreachable":
		return t.ErrorStyle.Render(status)
	}
	return status
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
