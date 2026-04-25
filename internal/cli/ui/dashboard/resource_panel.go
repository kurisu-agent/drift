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

// resourcePanel renders chest / characters / tunes — three read-only
// per-circuit resource lists that share the same shape (name, detail,
// used-by). One implementation, three identical wirings.
type resourcePanel struct {
	o     Options
	t     *ui.Theme
	title string
	fetch func(context.Context) ([]ResourceRow, error)
	tbl   table.Model
	rows  []ResourceRow
	err   string
	ready bool
}

func newResourcePanel(o Options, title string,
	fetch func(context.Context) ([]ResourceRow, error)) Panel {
	cols := []table.Column{
		{Title: "circuit", Width: 12},
		{Title: "name", Width: 22},
		{Title: "detail", Width: 36},
		{Title: "used by", Width: 22},
	}
	tbl := table.New(table.WithColumns(cols), table.WithFocused(true))
	tbl.SetStyles(tableStyles(o.Theme))
	return &resourcePanel{o: o, t: o.Theme, title: title, fetch: fetch, tbl: tbl}
}

func (p *resourcePanel) Title() string         { return p.title }
func (p *resourcePanel) KeyboardLocked() bool  { return false }
func (p *resourcePanel) ShortHelp() []key.Binding {
	return []key.Binding{ui.Keys.Up, ui.Keys.Down}
}

func (p *resourcePanel) Init() tea.Cmd { return p.refreshCmd() }

type resourceLoadedMsg struct {
	title string
	rows  []ResourceRow
}
type resourceErrMsg struct {
	title, err string
}

func (p *resourcePanel) refreshCmd() tea.Cmd {
	title, fetch := p.title, p.fetch
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
		if m.title != p.title {
			break
		}
		p.rows = m.rows
		p.tbl.SetRows(toResourceTableRows(m.rows))
		p.err = ""
		p.ready = true
		return p, nil
	case resourceErrMsg:
		if m.title != p.title {
			break
		}
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

func (p *resourcePanel) View(width, height int) string {
	if p.err != "" {
		return panelError(p.t, p.err, width, height)
	}
	if !p.ready {
		return panelEmpty(p.t, fmt.Sprintf("loading %s...", p.title), width, height)
	}
	if len(p.rows) == 0 {
		return panelEmpty(p.t, fmt.Sprintf("no %s yet — drift connect <circuit> to author them", p.title), width, height)
	}

	hint := p.renderHint(width)
	hintH := lipgloss.Height(hint)
	tableHeight := height - hintH - 1
	if tableHeight < 3 {
		tableHeight = 3
	}
	p.tbl.SetWidth(width)
	p.tbl.SetHeight(tableHeight)
	if hint == "" {
		return p.tbl.View()
	}
	return lipgloss.JoinVertical(lipgloss.Left, hint, "", p.tbl.View())
}

// renderHint is the one-line muted strip above the table that names
// the lakitu-side authoring command. The chest/characters/tunes panels
// are read-only views; mutations live server-side, and this hint
// keeps that contract visible without dragging the user out of the
// dashboard.
func (p *resourcePanel) renderHint(_ int) string {
	if p.t == nil || !p.t.Enabled {
		return fmt.Sprintf("authoring lives in lakitu — drift connect <circuit> -- %s add <name>", p.singular())
	}
	body := fmt.Sprintf("authoring lives in lakitu — drift connect <circuit> -- %s add <name>", p.singular())
	return p.t.MutedStyle.Render(body)
}

// singular returns the panel's resource word in subcommand form
// ("chest" → "chest", "characters" → "character", "tunes" → "tune"),
// matching the lakitu CLI surface.
func (p *resourcePanel) singular() string {
	switch p.title {
	case "characters":
		return "character"
	case "tunes":
		return "tune"
	}
	return p.title
}

func toResourceTableRows(rs []ResourceRow) []table.Row {
	out := make([]table.Row, len(rs))
	for i, r := range rs {
		out[i] = table.Row{r.Circuit, r.Name, truncate(r.Description, 36), r.UsedBy}
	}
	return out
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
