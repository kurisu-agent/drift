package dashboard

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/kurisu-agent/drift/internal/cli/ui"
)

// portsPanel renders the workstation-side port forwards. Plan 13 already
// owns the data layer (`drift ports`); this panel just shows it. The
// authoring half (add / remove flow) is intentionally not in this PR.
type portsPanel struct {
	o    Options
	t    *ui.Theme
	rows []PortRow
	err  string
}

func newPortsPanel(o Options) Panel { return &portsPanel{o: o, t: o.Theme} }

func (p *portsPanel) Title() string { return "ports" }

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
		p.err = ""
	case portsErrMsg:
		p.err = m.err
	case tickMsg:
		return p, p.refreshCmd()
	}
	return p, nil
}

func (p *portsPanel) View(width, height int) string {
	if p.err != "" {
		return p.t.ErrorStyle.Render("error: ") + p.err
	}
	if len(p.rows) == 0 {
		return p.t.DimStyle.Render("(no port forwards configured; see `drift ports`)")
	}
	var b strings.Builder
	b.WriteString(p.t.BoldStyle.Render(fmt.Sprintf("%-7s %-7s %-14s %-22s %s",
		"LOCAL", "REMOTE", "CIRCUIT", "KART", "STATE")))
	b.WriteString("\n")
	b.WriteString(p.t.DimStyle.Render(strings.Repeat("─", maxInt(20, width-2))))
	b.WriteString("\n")
	for _, r := range p.rows {
		state := p.t.DimStyle.Render("idle")
		if r.Active {
			state = p.t.SuccessStyle.Render("forwarding")
		}
		b.WriteString(fmt.Sprintf("%-7d %-7d %-14s %-22s %s\n",
			r.Local, r.Remote, r.Circuit, r.Kart, state))
	}
	_ = height
	return b.String()
}
