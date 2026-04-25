package dashboard

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/kurisu-agent/drift/internal/cli/ui"
)

// logsPanel renders a kart-scoped log tail. Data wiring is deferred
// (plan-16 non-goal); demo fixtures drive the rebrand work for now.
// The chrome shape is the deliverable: kart picker, follow indicator,
// filter strip, level-coloured viewport rows.
type logsPanel struct {
	o      Options
	t      *ui.Theme
	kart   string
	follow bool
	lines  []logLine

	filter    textinput.Model
	filtering bool
}

// logLine is the minimal record the panel renders. Real wiring will
// stream lines off the kart's stdout/stderr; the demo fixture below
// is a static slice covering the level set we care about.
type logLine struct {
	When    string // pre-formatted timestamp ("14:00:12")
	Level   string // debug | info | warn | error
	Message string
}

func newLogsPanel(o Options) Panel {
	in := textinput.New()
	in.Prompt = "/ "
	in.Placeholder = "filter logs (esc to clear)"
	in.SetVirtualCursor(true)
	follow := true
	if o.LogsFollowDefault != nil {
		follow = *o.LogsFollowDefault
	}
	return &logsPanel{
		o:      o,
		t:      o.Theme,
		kart:   "alpha.api",
		follow: follow,
		lines:  demoLogLines(),
		filter: in,
	}
}

func (p *logsPanel) Title() string { return "logs" }

func (p *logsPanel) ShortHelp() []key.Binding {
	if p.filtering {
		return []key.Binding{ui.Keys.Escape}
	}
	return []key.Binding{ui.Keys.Up, ui.Keys.Down, ui.Keys.Filter}
}

func (p *logsPanel) Init() tea.Cmd {
	if p.o.InitialFilter != "" {
		p.filter.SetValue(p.o.InitialFilter)
	}
	return nil
}

func (p *logsPanel) Update(msg tea.Msg) (Panel, tea.Cmd) {
	if m, ok := msg.(tea.KeyPressMsg); ok {
		if p.filtering {
			switch {
			case key.Matches(m, ui.Keys.Escape):
				p.filtering = false
				p.filter.Blur()
				p.filter.Reset()
				return p, nil
			case m.String() == "enter":
				p.filtering = false
				p.filter.Blur()
				return p, nil
			}
			var cmd tea.Cmd
			p.filter, cmd = p.filter.Update(msg)
			return p, cmd
		}
		switch {
		case key.Matches(m, ui.Keys.Filter):
			p.filtering = true
			cmd := p.filter.Focus()
			return p, cmd
		case m.String() == "f":
			// Toggle follow mode (live runs would also pause/resume the
			// streaming source; the demo just flips the badge).
			p.follow = !p.follow
			return p, nil
		}
	}
	return p, nil
}

func (p *logsPanel) View(width, height int) string {
	header := p.renderHeader(width)
	chrome := ""
	if p.filtering || p.filter.Value() != "" {
		chrome = p.renderFilterChrome(width)
	}

	bodyHeight := height - lipgloss.Height(header) - 1
	if chrome != "" {
		bodyHeight -= lipgloss.Height(chrome) + 1
	}
	if bodyHeight < 1 {
		bodyHeight = 1
	}

	body := p.renderBody(width, bodyHeight)
	parts := []string{header, ""}
	if chrome != "" {
		parts = append(parts, chrome, "")
	}
	parts = append(parts, body)
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// renderHeader is the strip above the body: kart name on the left,
// follow / paused badge flush right. The kart-picker UX (cycling
// through karts via h/l) lives behind real data and lands later.
func (p *logsPanel) renderHeader(width int) string {
	kartLabel := ui.Label(ui.IconKart, p.kart)
	follow := "○ paused"
	if p.follow {
		follow = "● follow"
	}
	if p.t != nil && p.t.Enabled {
		kartLabel = p.t.AccentStyle.Render(kartLabel)
		if p.follow {
			follow = p.t.Status.Success.Text.Render(follow)
		} else {
			follow = p.t.MutedStyle.Render(follow)
		}
	}
	pad := width - lipgloss.Width(kartLabel) - lipgloss.Width(follow)
	if pad < 1 {
		pad = 1
	}
	return kartLabel + strings.Repeat(" ", pad) + follow
}

// renderFilterChrome mirrors the karts panel's strip: textinput on
// the left, match-count flush right.
func (p *logsPanel) renderFilterChrome(width int) string {
	count := logMatchCount(p.lines, p.filter.Value())
	hint := fmt.Sprintf("%d/%d match", count, len(p.lines))
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

// renderBody lays out the visible log lines in the available height.
// Lines that don't match the filter render dim instead of being
// removed (rubric: "Filter dims non-matches inline").
func (p *logsPanel) renderBody(width, height int) string {
	if len(p.lines) == 0 {
		return panelEmpty(p.t, "no logs yet for this kart.", width, height)
	}
	visible := p.lines
	if len(visible) > height {
		visible = visible[len(visible)-height:]
	}
	rendered := make([]string, len(visible))
	q := strings.ToLower(p.filter.Value())
	for i, l := range visible {
		match := q == "" ||
			strings.Contains(strings.ToLower(l.Message), q) ||
			strings.Contains(strings.ToLower(l.Level), q)
		rendered[i] = renderLogLine(l, match, p.t)
	}
	return strings.Join(rendered, "\n")
}

// renderLogLine produces one row: timestamp (muted), level pill,
// message (default text). Non-match rows wrap the whole row in dim.
func renderLogLine(l logLine, match bool, t *ui.Theme) string {
	timestamp := l.When
	level := l.Level
	msg := l.Message
	if t != nil && t.Enabled {
		timestamp = t.MutedStyle.Render(timestamp)
		level = renderLogLevel(l.Level, t)
	}
	row := timestamp + "  " + level + "  " + msg
	if !match && t != nil && t.Enabled {
		// Strip inner ANSI before redimming so dim is the only layer.
		raw := stripStyleHints(row)
		return t.DimStyle.Render(raw)
	}
	return row
}

// renderLogLevel maps level → status chrome. info / warn / error
// become pills (column-scan friendly); debug is muted text.
func renderLogLevel(level string, t *ui.Theme) string {
	switch level {
	case "info":
		return t.Status.Info.Pill.Render("info ")
	case "warn":
		return t.Status.Warn.Pill.Render("warn ")
	case "error":
		return t.Status.Error.Pill.Render("error")
	default:
		return t.MutedStyle.Render("debug")
	}
}

func logMatchCount(lines []logLine, filter string) int {
	if filter == "" {
		return len(lines)
	}
	q := strings.ToLower(filter)
	n := 0
	for _, l := range lines {
		if strings.Contains(strings.ToLower(l.Message), q) ||
			strings.Contains(strings.ToLower(l.Level), q) {
			n++
		}
	}
	return n
}

// demoLogLines is the static fixture that drives the logs panel until
// real data wiring lands. Levels cover the full set the rendering
// path styles; messages mimic the cadence of a Go service the user
// might be running on a kart.
func demoLogLines() []logLine {
	return []logLine{
		{When: "13:58:42", Level: "info", Message: "starting api server on :8080"},
		{When: "13:58:42", Level: "debug", Message: "loaded 7 routes from internal/api/routes.go"},
		{When: "13:58:43", Level: "info", Message: "connected to postgres dsn=alpha.db"},
		{When: "13:59:11", Level: "info", Message: "GET /healthz 200 1.2ms"},
		{When: "13:59:18", Level: "warn", Message: "deprecated header X-Drift-Old in request from 10.0.0.7"},
		{When: "13:59:42", Level: "info", Message: "POST /v1/sessions 201 8.4ms"},
		{When: "14:00:00", Level: "info", Message: "tick: 0.1MB/s in, 0.4MB/s out, 13 connections"},
		{When: "14:00:12", Level: "error", Message: "downstream lakitu auth refused: token expired"},
		{When: "14:00:12", Level: "info", Message: "retrying with refreshed token (attempt 1/3)"},
		{When: "14:00:13", Level: "info", Message: "downstream lakitu auth ok"},
		{When: "14:00:30", Level: "debug", Message: "scheduler: 3 jobs pending, 2 running, 8 done"},
		{When: "14:00:45", Level: "warn", Message: "memory pressure: 412MB / 512MB rss"},
	}
}
