package dashboard

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/kurisu-agent/drift/internal/cli/ui"
)

type circuitsPanel struct {
	o    Options
	t    *ui.Theme
	rows []CircuitRow
	err  string
}

func newCircuitsPanel(o Options) Panel { return &circuitsPanel{o: o, t: o.Theme} }

func (p *circuitsPanel) Title() string { return "circuits" }

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
		p.err = ""
	case circuitsErrMsg:
		p.err = m.err
	case tickMsg:
		return p, p.refreshCmd()
	}
	return p, nil
}

func (p *circuitsPanel) View(width, height int) string {
	if p.err != "" {
		return p.t.ErrorStyle.Render("error: ") + p.err
	}
	if len(p.rows) == 0 {
		return p.t.DimStyle.Render("(no circuits configured; try `drift circuit add user@host`)")
	}
	var b strings.Builder
	b.WriteString(p.t.BoldStyle.Render(fmt.Sprintf("%-18s %-30s %-12s %-10s %s",
		"NAME", "HOST", "LAKITU", "LATENCY", "STATE")))
	b.WriteString("\n")
	b.WriteString(p.t.DimStyle.Render(strings.Repeat("─", maxInt(20, width-2))))
	b.WriteString("\n")
	for _, r := range p.rows {
		state := p.t.SuccessStyle.Render("reachable")
		if !r.Reachable {
			state = p.t.ErrorStyle.Render("unreachable")
		}
		def := ""
		if r.Default {
			def = p.t.AccentStyle.Render(" *default")
		}
		latency := "—"
		if r.LatencyMS > 0 {
			latency = fmt.Sprintf("%dms", r.LatencyMS)
		}
		fmt.Fprintf(&b, "%-18s %-30s %-12s %-10s %s%s\n",
			r.Name, r.Host, dashIfEmpty(r.Lakitu), latency, state, def)
	}
	_ = height
	return b.String()
}
