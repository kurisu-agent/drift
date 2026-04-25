package dashboard

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/kurisu-agent/drift/internal/cli/ui"
)

// logsPanel is a placeholder. The full design has a kart picker plus
// scrollable viewport plus filter / level / follow controls; this PR
// lands the panel scaffold so the navigation works while the data hooks
// catch up.
type logsPanel struct {
	o Options
	t *ui.Theme
}

func newLogsPanel(o Options) Panel { return &logsPanel{o: o, t: o.Theme} }

func (p *logsPanel) Title() string                     { return "logs" }
func (p *logsPanel) ShortHelp() []key.Binding          { return nil }
func (p *logsPanel) Init() tea.Cmd                     { return nil }
func (p *logsPanel) Update(_ tea.Msg) (Panel, tea.Cmd) { return p, nil }

func (p *logsPanel) View(width, height int) string {
	return panelEmpty(p.t, "log tail not wired in this view yet. run `drift logs <kart>` from a shell.", width, height)
}
