package dashboard

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/table"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/kurisu-agent/drift/internal/cli/ui"
)

type kartsPanel struct {
	o     Options
	t     *ui.Theme
	tbl   table.Model
	rows  []KartRow
	err   string
	ready bool

	filter    textinput.Model
	filtering bool
}

func newKartsPanel(o Options) Panel {
	cols := []table.Column{
		{Title: "circuit", Width: 12},
		{Title: "name", Width: 20},
		{Title: "status", Width: 14},
		{Title: "tune", Width: 12},
		{Title: "source", Width: 10},
		{Title: "autostart", Width: 9},
	}
	tbl := table.New(table.WithColumns(cols), table.WithFocused(true))
	tbl.SetStyles(tableStyles(o.Theme))

	in := textinput.New()
	in.Prompt = "/ "
	in.Placeholder = "filter karts (esc to clear)"
	in.SetVirtualCursor(true)
	return &kartsPanel{o: o, t: o.Theme, tbl: tbl, filter: in}
}

func (p *kartsPanel) Title() string         { return "karts" }
func (p *kartsPanel) KeyboardLocked() bool  { return p.filtering }

func (p *kartsPanel) ShortHelp() []key.Binding {
	if p.filtering {
		return []key.Binding{ui.Keys.Escape}
	}
	return []key.Binding{ui.Keys.Up, ui.Keys.Down, ui.Keys.Filter}
}

func (p *kartsPanel) Init() tea.Cmd {
	if p.o.InitialFilter != "" {
		p.filter.SetValue(p.o.InitialFilter)
	}
	return p.refreshCmd()
}

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
		p.tbl.SetRows(toKartTableRows(m.rows, p.t, p.filter.Value()))
		p.err = ""
		p.ready = true
		return p, nil
	case kartsErrMsg:
		p.err = m.err
		return p, nil
	case tickMsg:
		return p, p.refreshCmd()
	case tea.KeyPressMsg:
		if p.filtering {
			switch {
			case key.Matches(m, ui.Keys.Escape):
				p.filtering = false
				p.filter.Blur()
				p.filter.Reset()
				p.tbl.SetRows(toKartTableRows(p.rows, p.t, ""))
				return p, nil
			case m.String() == "enter":
				p.filtering = false
				p.filter.Blur()
				return p, nil
			}
			var cmd tea.Cmd
			p.filter, cmd = p.filter.Update(msg)
			p.tbl.SetRows(toKartTableRows(p.rows, p.t, p.filter.Value()))
			return p, cmd
		}
		switch {
		case key.Matches(m, ui.Keys.Filter):
			p.filtering = true
			cmd := p.filter.Focus()
			return p, cmd
		case m.String() == "r":
			return p, p.refreshCmd()
		}
	}
	if p.filtering {
		return p, nil
	}
	var cmd tea.Cmd
	p.tbl, cmd = p.tbl.Update(msg)
	return p, cmd
}

func (p *kartsPanel) View(width, height int) string {
	if p.err != "" {
		return panelError(p.t, p.err, width, height)
	}
	if !p.ready {
		return panelEmpty(p.t, "loading karts...", width, height)
	}
	if len(p.rows) == 0 {
		return panelEmpty(p.t, "no karts yet, drift new to create one", width, height)
	}

	tableHeight := height
	chrome := ""
	if p.filtering || p.filter.Value() != "" {
		chrome = p.renderFilterChrome(width)
		tableHeight -= lipgloss.Height(chrome) + 1
	}
	if tableHeight < 3 {
		tableHeight = 3
	}
	p.tbl.SetWidth(width)
	p.tbl.SetHeight(tableHeight)

	if chrome == "" {
		return p.tbl.View()
	}
	return lipgloss.JoinVertical(lipgloss.Left, chrome, "", p.tbl.View())
}

// renderFilterChrome is the one-line strip above the table during
// filter mode. The textinput exposes its own cursor; the trailing
// match-count hint sits flush right.
func (p *kartsPanel) renderFilterChrome(width int) string {
	count := matchCount(p.rows, p.filter.Value())
	hint := fmt.Sprintf("%d/%d match", count, len(p.rows))
	if p.t != nil && p.t.Enabled {
		hint = p.t.MutedStyle.Render(hint)
	}
	p.filter.SetWidth(width - lipgloss.Width(hint) - 4)
	left := p.filter.View()
	pad := width - lipgloss.Width(left) - lipgloss.Width(hint) - 1
	if pad < 1 {
		pad = 1
	}
	return left + strings.Repeat(" ", pad) + hint
}

// toKartTableRows converts kart records into the table.Row form,
// applying the row-level dim styling for filter non-matches. When
// filter is empty every row renders at default emphasis; with a
// filter, matches stay default and non-matches dim — the rubric's
// "non-matching rows rendered dim, not removed" pattern.
func toKartTableRows(rs []KartRow, t *ui.Theme, filter string) []table.Row {
	out := make([]table.Row, len(rs))
	for i, r := range rs {
		auto := "—"
		if r.Autostart {
			auto = "yes"
		}
		match := rowMatches(r, filter)
		cells := table.Row{
			r.Circuit,
			r.Name,
			styleStatus(t, r.Status),
			dashIfEmpty(r.Tune),
			dashIfEmpty(r.Source),
			auto,
		}
		if !match && t != nil && t.Enabled {
			for j := range cells {
				cells[j] = t.DimStyle.Render(stripStyleHints(cells[j]))
			}
		}
		out[i] = cells
	}
	return out
}

// stripStyleHints undoes the inner pill/dim styling on a cell so
// non-matches render uniformly dim instead of leaking the prior
// pill background through. Keeps the dim layer the only ANSI on
// the row.
func stripStyleHints(s string) string {
	if !strings.ContainsRune(s, 0x1b) {
		return s
	}
	// Best-effort strip: remove ANSI CSI sequences. We only call this
	// path on filter non-matches, so the cost is bounded.
	var b strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == 0x1b:
			in = true
		case in && r == 'm':
			in = false
		case in:
			// drop intermediate
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// matchCount returns how many rows match the filter. Empty filter
// matches all.
func matchCount(rs []KartRow, filter string) int {
	if filter == "" {
		return len(rs)
	}
	n := 0
	for _, r := range rs {
		if rowMatches(r, filter) {
			n++
		}
	}
	return n
}

// rowMatches is a case-insensitive substring search over the kart's
// circuit and name fields. Empty filter matches everything.
func rowMatches(r KartRow, filter string) bool {
	if filter == "" {
		return true
	}
	q := strings.ToLower(filter)
	return strings.Contains(strings.ToLower(r.Circuit), q) ||
		strings.Contains(strings.ToLower(r.Name), q) ||
		strings.Contains(strings.ToLower(r.Status), q)
}

// styleStatus renders a kart's status as a pill — bold + bg-tinted +
// padded so the column reads at a glance. running/stopped/stale/error
// each map to a distinct status pill; unknown values fall through as
// plain text. Plan-16 brand guidelines: pills are reserved for
// scan-a-column moments, of which the kart status column is the
// canonical example.
func styleStatus(t *ui.Theme, status string) string {
	if t == nil || !t.Enabled {
		return status
	}
	switch status {
	case "running":
		return t.Status.Success.Pill.Render(status)
	case "stopped":
		return t.MutedStyle.Render(status)
	case "stale":
		return t.Status.Warn.Pill.Render(status)
	case "error", "unreachable":
		return t.Status.Error.Pill.Render(status)
	}
	return status
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
