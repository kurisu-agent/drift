package dashboard

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/kurisu-agent/drift/internal/cli/ui"
)

// logsPanel is a minimal placeholder. The full design has a kart picker
// + scrollable viewport + filter / level / follow controls; this PR
// lands the panel scaffold so the navigation works while the data hooks
// catch up in a follow-up.
type logsPanel struct {
	o Options
	t *ui.Theme
}

func newLogsPanel(o Options) Panel { return &logsPanel{o: o, t: o.Theme} }

func (p *logsPanel) Title() string { return "logs" }

func (p *logsPanel) Init() tea.Cmd { return nil }

func (p *logsPanel) Update(msg tea.Msg) (Panel, tea.Cmd) { return p, nil }

func (p *logsPanel) View(width, height int) string {
	var b strings.Builder
	b.WriteString(p.t.BoldStyle.Render("logs"))
	b.WriteString("\n\n")
	b.WriteString(p.t.DimStyle.Render("interactive log tail not yet wired in this view."))
	b.WriteString("\n")
	b.WriteString(p.t.DimStyle.Render("for now, run `drift logs <kart>` from a shell."))
	_ = width
	_ = height
	return b.String()
}
