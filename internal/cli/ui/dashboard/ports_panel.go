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

// portsPanel renders the workstation-side port forwards. Plan 13 owns
// the data layer (`drift ports`); this panel reads from it via the
// shared DataSource. The authoring half (add / remove flow) lands in a
// follow-up.
type portsPanel struct {
	o     Options
	t     *ui.Theme
	tbl   table.Model
	rows  []PortRow
	err   string
	ready bool
}

func newPortsPanel(o Options) Panel {
	cols := []table.Column{
		{Title: "·", Width: 2},
		{Title: "forward", Width: 14},
		{Title: "circuit", Width: 12},
		{Title: "kart", Width: 22},
		{Title: "state", Width: 14},
	}
	tbl := table.New(table.WithColumns(cols), table.WithFocused(true))
	tbl.SetStyles(tableStyles(o.Theme))
	return &portsPanel{o: o, t: o.Theme, tbl: tbl}
}

func (p *portsPanel) Title() string { return "ports" }
func (p *portsPanel) ShortHelp() []key.Binding {
	return []key.Binding{ui.Keys.Up, ui.Keys.Down}
}

func (p *portsPanel) Init() tea.Cmd { return p.refreshCmd() }

type portsLoadedMsg struct{ rows []PortRow }
type portsErrMsg struct{ err string }

func (p *portsPanel) refreshCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		rows, err := p.o.DataSource.Ports(ctx)
		if err != nil {
			return portsErrMsg{err: err.Error()}
		}
		return portsLoadedMsg{rows: rows}
	}
}

func (p *portsPanel) Update(msg tea.Msg) (Panel, tea.Cmd) {
	switch m := msg.(type) {
	case portsLoadedMsg:
		p.rows = m.rows
		p.tbl.SetRows(toPortTableRows(m.rows, p.t))
		p.err = ""
		p.ready = true
		return p, nil
	case portsErrMsg:
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

func (p *portsPanel) View(width, height int) string {
	if p.err != "" {
		return panelError(p.t, p.err, width, height)
	}
	if !p.ready {
		return panelEmpty(p.t, "loading ports...", width, height)
	}
	if len(p.rows) == 0 {
		return panelEmpty(p.t, "no port forwards yet. see `drift ports add`.", width, height)
	}
	p.tbl.SetWidth(width)
	p.tbl.SetHeight(maxInt(3, height-2))
	return p.tbl.View()
}

// toPortTableRows builds the table rows. Direction is rendered as a
// single arrow-glyph column ("3000 → 3000") rather than two number
// columns; conflicts on host port are detected by counting Local
// occurrences and styled as warn pills with a leading warning glyph.
func toPortTableRows(rs []PortRow, t *ui.Theme) []table.Row {
	conflicts := map[int]int{}
	for _, r := range rs {
		conflicts[r.Local]++
	}

	out := make([]table.Row, len(rs))
	for i, r := range rs {
		out[i] = table.Row{
			portMarker(conflicts[r.Local] > 1, t),
			fmt.Sprintf("%d → %d", r.Local, r.Remote),
			r.Circuit,
			r.Kart,
			portState(r.Active, conflicts[r.Local] > 1, t),
		}
	}
	return out
}

// portMarker renders the leading warning glyph for conflicting rows.
// Empty cell otherwise so the column rhythm stays even.
func portMarker(conflict bool, t *ui.Theme) string {
	if !conflict {
		return " "
	}
	if t == nil || !t.Enabled {
		return ui.IconWarning
	}
	return lipgloss.NewStyle().
		Foreground(t.Status.Warn.Text.GetForeground()).
		Render(ui.IconWarning)
}

// portState picks the right pill for a row: conflict overrides
// active (a forwarding row that conflicts is still in trouble);
// otherwise active → success pill, idle → muted text.
func portState(active, conflict bool, t *ui.Theme) string {
	switch {
	case conflict:
		if t == nil || !t.Enabled {
			return "conflict"
		}
		return t.Status.Warn.Pill.Render("conflict")
	case active:
		if t == nil || !t.Enabled {
			return "forwarding"
		}
		return t.Status.Success.Pill.Render("forwarding")
	default:
		if t == nil || !t.Enabled {
			return "idle"
		}
		return t.MutedStyle.Render("idle")
	}
}
