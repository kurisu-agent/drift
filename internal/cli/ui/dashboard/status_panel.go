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

type statusPanel struct {
	o    Options
	t    *ui.Theme
	tbl  table.Model
	snap StatusSnapshot
	err  string
}

func newStatusPanel(o Options) Panel {
	cols := []table.Column{
		{Title: "time", Width: 10},
		{Title: "action", Width: 16},
		{Title: "kart", Width: 24},
		{Title: "detail", Width: 40},
	}
	tbl := table.New(table.WithColumns(cols), table.WithFocused(false))
	tbl.SetStyles(tableStyles(o.Theme))
	return &statusPanel{o: o, t: o.Theme, tbl: tbl}
}

func (p *statusPanel) Title() string            { return "status" }
func (p *statusPanel) ShortHelp() []key.Binding { return nil }
func (p *statusPanel) Init() tea.Cmd            { return p.refreshCmd() }

func (p *statusPanel) refreshCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		snap, err := p.o.DataSource.Status(ctx)
		if err != nil {
			return statusErrMsg{err: err.Error()}
		}
		return statusSnapMsg{snap: snap}
	}
}

type statusSnapMsg struct{ snap StatusSnapshot }
type statusErrMsg struct{ err string }

func (p *statusPanel) Update(msg tea.Msg) (Panel, tea.Cmd) {
	switch m := msg.(type) {
	case statusSnapMsg:
		p.snap = m.snap
		p.err = ""
		p.tbl.SetRows(activityRows(m.snap.Activity, time.Now(), p.t))
	case statusErrMsg:
		p.err = m.err
	case tickMsg:
		return p, p.refreshCmd()
	case tea.KeyPressMsg:
		if m.String() == "r" {
			return p, p.refreshCmd()
		}
	}
	return p, nil
}

func (p *statusPanel) View(width, height int) string {
	header := p.headerRow(width)
	if p.err != "" {
		return lipgloss.JoinVertical(lipgloss.Left,
			header,
			panelError(p.t, p.err, width, maxInt(1, height-lipgloss.Height(header))),
		)
	}

	tableHeight := maxInt(3, height-lipgloss.Height(header)-1)
	p.tbl.SetWidth(width)
	p.tbl.SetHeight(tableHeight)
	body := p.tbl.View()
	if len(p.snap.Activity) == 0 {
		body = panelEmpty(p.t, "no recent activity yet.", width, tableHeight)
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, body)
}

// headerRow lays out the banner block (left) + stats block (right) on
// one row, sized to fit width.
func (p *statusPanel) headerRow(width int) string {
	stats := p.statsBlock()
	statsW := lipgloss.Width(stats)
	leftW := maxInt(0, width-statsW-2)

	bannerStyle := lipgloss.NewStyle().Width(leftW).Padding(0, 1)
	banner := bannerStyle.Render(p.banner())

	return lipgloss.JoinHorizontal(lipgloss.Top, banner, stats)
}

func (p *statusPanel) banner() string {
	v := p.snap.DriftVersion
	if v == "" {
		v = p.o.DriftVersion
	}
	bold := boldFor(p.t)
	dim := dimFor(p.t)
	return lipgloss.JoinVertical(lipgloss.Left,
		bold.Render(fmt.Sprintf("drift %s", v)),
		dim.Render("devpods for drifters"),
		"",
	)
}

func (p *statusPanel) statsBlock() string {
	bold := boldFor(p.t)
	dim := dimFor(p.t)
	rows := []struct{ num, label string }{
		{fmt.Sprintf("%d / %d", p.snap.CircuitsReachable, p.snap.CircuitsTotal), "circuits"},
		{fmt.Sprintf("%d / %d", p.snap.KartsRunning, p.snap.KartsTotal), "karts"},
		{fmt.Sprintf("%d", p.snap.PortsActive), "ports"},
	}
	numW := 0
	for _, r := range rows {
		if w := lipgloss.Width(r.num); w > numW {
			numW = w
		}
	}
	lines := make([]string, len(rows))
	for i, r := range rows {
		num := lipgloss.NewStyle().Width(numW).Align(lipgloss.Right).Render(bold.Render(r.num))
		lines[i] = lipgloss.JoinHorizontal(lipgloss.Top, num, "  ", dim.Render(r.label))
	}
	return lipgloss.NewStyle().Padding(0, 1).Render(lipgloss.JoinVertical(lipgloss.Right, lines...))
}

func activityRows(entries []ActivityEntry, now time.Time, t *ui.Theme) []table.Row {
	dim := dimFor(t)
	out := make([]table.Row, 0, len(entries))
	for _, e := range entries {
		kart := e.Kart
		if kart == "" {
			kart = "—"
		}
		out = append(out, table.Row{
			dim.Render(relTime(now, e.When)),
			e.Action,
			kart,
			dim.Render(e.Detail),
		})
	}
	return out
}

func relTime(now, t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
