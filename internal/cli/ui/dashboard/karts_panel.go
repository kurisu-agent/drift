package dashboard

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/kurisu-agent/drift/internal/cli/ui"
)

type kartsPanel struct {
	o    Options
	t    *ui.Theme
	rows []KartRow
	err  string
	cur  int
	last time.Time
}

func newKartsPanel(o Options) Panel {
	return &kartsPanel{o: o, t: o.Theme}
}

func (p *kartsPanel) Title() string { return "karts" }

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
		if p.cur >= len(p.rows) {
			p.cur = 0
		}
		p.err = ""
		p.last = time.Now()
	case kartsErrMsg:
		p.err = m.err
	case tickMsg:
		return p, p.refreshCmd()
	case tea.KeyMsg:
		switch m.String() {
		case "j", "down":
			if p.cur+1 < len(p.rows) {
				p.cur++
			}
		case "k", "up":
			if p.cur > 0 {
				p.cur--
			}
		case "r":
			return p, p.refreshCmd()
		}
	}
	return p, nil
}

func (p *kartsPanel) View(width, height int) string {
	if p.err != "" {
		return p.t.ErrorStyle.Render("error: ") + p.err
	}
	if len(p.rows) == 0 {
		return p.t.DimStyle.Render("(no karts on any configured circuit)")
	}
	var b strings.Builder
	b.WriteString(p.t.BoldStyle.Render(fmt.Sprintf("%-14s %-22s %-10s %-12s %s",
		"CIRCUIT", "NAME", "STATUS", "TUNE", "SOURCE")))
	b.WriteString("\n")
	b.WriteString(p.t.DimStyle.Render(strings.Repeat("─", maxInt(20, width-2))))
	b.WriteString("\n")
	for i, r := range p.rows {
		marker := "  "
		if i == p.cur {
			marker = p.t.AccentStyle.Render(ui.Icon(ui.IconArrow)) + " "
		}
		status := r.Status
		switch r.Status {
		case "running":
			status = p.t.SuccessStyle.Render(r.Status)
		case "stopped":
			status = p.t.DimStyle.Render(r.Status)
		case "stale":
			status = p.t.WarnStyle.Render(r.Status)
		case "error", "unreachable":
			status = p.t.ErrorStyle.Render(r.Status)
		}
		b.WriteString(fmt.Sprintf("%s%-14s %-22s %-10s %-12s %s\n",
			marker, r.Circuit, r.Name, status, dashIfEmpty(r.Tune), dashIfEmpty(r.Source)))
	}
	_ = height
	return b.String()
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
