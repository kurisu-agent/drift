package dashboard

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/kurisu-agent/drift/internal/cli/ui"
)

type statusPanel struct {
	o    Options
	t    *ui.Theme
	snap StatusSnapshot
	err  string
	last time.Time
}

func newStatusPanel(o Options) Panel {
	return &statusPanel{o: o, t: o.Theme}
}

func (p *statusPanel) Title() string { return "status" }

func (p *statusPanel) Init() tea.Cmd { return p.refreshCmd() }

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
		p.last = time.Now()
	case statusErrMsg:
		p.err = m.err
	case tickMsg:
		return p, p.refreshCmd()
	}
	return p, nil
}

func (p *statusPanel) View(width, height int) string {
	var b strings.Builder
	header := p.t.BoldStyle.Render(fmt.Sprintf("drift %s", p.driftVersion()))
	tagline := p.t.DimStyle.Render("devpods for drifters")

	statsCol := p.statsBlock()
	left := lipgloss.JoinVertical(lipgloss.Left, header, tagline)
	top := lipgloss.JoinHorizontal(lipgloss.Top,
		lipgloss.NewStyle().Width(maxInt(0, width-lipgloss.Width(statsCol)-2)).Render(left),
		statsCol,
	)
	b.WriteString(top)
	b.WriteString("\n\n")

	if p.err != "" {
		b.WriteString(p.t.ErrorStyle.Render("error: ") + p.err + "\n")
	}
	b.WriteString(p.activityTable(width, maxInt(5, height-6)))
	return b.String()
}

func (p *statusPanel) driftVersion() string {
	if p.snap.DriftVersion != "" {
		return p.snap.DriftVersion
	}
	return p.o.DriftVersion
}

func (p *statusPanel) statsBlock() string {
	rows := []struct {
		num   string
		label string
	}{
		{fmt.Sprintf("%d / %d", p.snap.CircuitsReachable, p.snap.CircuitsTotal), "circuits"},
		{fmt.Sprintf("%d / %d", p.snap.KartsRunning, p.snap.KartsTotal), "karts"},
		{fmt.Sprintf("%d", p.snap.PortsActive), "ports"},
	}
	maxNum := 0
	for _, r := range rows {
		if n := lipgloss.Width(r.num); n > maxNum {
			maxNum = n
		}
	}
	var lines []string
	for _, r := range rows {
		num := strings.Repeat(" ", maxNum-lipgloss.Width(r.num)) + r.num
		lines = append(lines, p.t.BoldStyle.Render(num)+"   "+p.t.DimStyle.Render(r.label))
	}
	return lipgloss.JoinVertical(lipgloss.Right, lines...)
}

func (p *statusPanel) activityTable(width, height int) string {
	if len(p.snap.Activity) == 0 {
		return p.t.DimStyle.Render("(no recent activity)")
	}
	var b strings.Builder
	headerStyle := p.t.BoldStyle
	b.WriteString(headerStyle.Render(fmt.Sprintf("%-10s %-18s %-22s %s", "TIME", "ACTION", "KART", "DETAIL")))
	b.WriteString("\n")
	b.WriteString(p.t.DimStyle.Render(strings.Repeat("─", maxInt(20, width-2))))
	b.WriteString("\n")
	limit := height
	if limit > len(p.snap.Activity) {
		limit = len(p.snap.Activity)
	}
	now := time.Now()
	for _, e := range p.snap.Activity[:limit] {
		when := relTime(now, e.When)
		kart := e.Kart
		if kart == "" {
			kart = "—"
		}
		fmt.Fprintf(&b, "%-10s %-18s %-22s %s\n",
			p.t.DimStyle.Render(when), e.Action, kart, p.t.DimStyle.Render(e.Detail))
	}
	return b.String()
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
