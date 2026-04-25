package dashboard

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/kurisu-agent/drift/internal/cli/ui"
)

// resourcePanel renders chest / characters / tunes — three read-only
// per-circuit resource lists that share the same shape.
type resourcePanel struct {
	o      Options
	t      *ui.Theme
	title  string
	header string
	fetch  func(context.Context) ([]ResourceRow, error)
	rows   []ResourceRow
	err    string
}

func newResourcePanel(o Options, title, header string,
	fetch func(context.Context) ([]ResourceRow, error)) Panel {
	return &resourcePanel{o: o, t: o.Theme, title: title, header: header, fetch: fetch}
}

func (p *resourcePanel) Title() string { return p.title }

func (p *resourcePanel) Init() tea.Cmd { return p.refreshCmd() }

type resourceLoadedMsg struct {
	title string
	rows  []ResourceRow
}
type resourceErrMsg struct {
	title string
	err   string
}

func (p *resourcePanel) refreshCmd() tea.Cmd {
	title := p.title
	fetch := p.fetch
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		rows, err := fetch(ctx)
		if err != nil {
			return resourceErrMsg{title: title, err: err.Error()}
		}
		return resourceLoadedMsg{title: title, rows: rows}
	}
}

func (p *resourcePanel) Update(msg tea.Msg) (Panel, tea.Cmd) {
	switch m := msg.(type) {
	case resourceLoadedMsg:
		if m.title == p.title {
			p.rows = m.rows
			p.err = ""
		}
	case resourceErrMsg:
		if m.title == p.title {
			p.err = m.err
		}
	case tickMsg:
		return p, p.refreshCmd()
	}
	return p, nil
}

func (p *resourcePanel) View(width, height int) string {
	if p.err != "" {
		return p.t.ErrorStyle.Render("error: ") + p.err
	}
	if len(p.rows) == 0 {
		return p.t.DimStyle.Render(fmt.Sprintf("(no %s defined on any configured circuit)", p.title))
	}
	var b strings.Builder
	b.WriteString(p.t.BoldStyle.Render(fmt.Sprintf("%-14s %-22s %-40s %s",
		"CIRCUIT", "NAME", "DETAIL", "USED-BY")))
	b.WriteString("\n")
	b.WriteString(p.t.DimStyle.Render(strings.Repeat("─", maxInt(20, width-2))))
	b.WriteString("\n")
	for _, r := range p.rows {
		fmt.Fprintf(&b, "%-14s %-22s %-40s %s\n",
			r.Circuit, r.Name, truncate(r.Description, 40), p.t.DimStyle.Render(r.UsedBy))
	}
	_ = height
	return b.String()
}

func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	if max <= 1 {
		return "…"
	}
	return s[:max-1] + "…"
}
