package dashboard

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/kurisu-agent/drift/internal/cli/ui"
)

type statusPanel struct {
	o    Options
	t    *ui.Theme
	tbl  table.Model
	snap StatusSnapshot
	err  string

	width int
	entr  *entrance
}

func newStatusPanel(o Options) Panel {
	cols := []table.Column{
		{Title: "time", Width: 10},
		{Title: "action", Width: 16},
		{Title: "kart", Width: 24},
		{Title: "detail", Width: 40},
	}
	tbl := table.New(table.WithColumns(cols), table.WithFocused(false))
	tbl.SetStyles(tableStyles(o.Theme))
	return &statusPanel{o: o, t: o.Theme, tbl: tbl}
}

func (p *statusPanel) Title() string            { return "status" }
func (p *statusPanel) ShortHelp() []key.Binding { return nil }

func (p *statusPanel) Init() tea.Cmd {
	// The frame loop kicks off here. The entrance object itself is
	// created lazily on the first WindowSizeMsg so we know the layout
	// width — the spring targets are width-relative.
	return tea.Batch(p.refreshCmd(), animFrameCmd())
}

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
		p.tbl.SetRows(activityRows(m.snap.Activity, time.Now(), p.t))
	case statusErrMsg:
		p.err = m.err
	case tea.WindowSizeMsg:
		p.width = m.Width
		if p.entr == nil {
			motionOff := p.t == nil || !p.t.Enabled || p.o.MotionDisabled
			p.entr = newEntrance(m.Width, motionOff)
		}
	case animFrameMsg:
		if p.entr == nil {
			return p, nil
		}
		if p.entr.tick() {
			return p, animFrameCmd()
		}
	case tickMsg:
		return p, p.refreshCmd()
	case tea.KeyPressMsg:
		if m.String() == "r" {
			return p, p.refreshCmd()
		}
	}
	return p, nil
}

func (p *statusPanel) View(width, height int) string {
	header := p.headerRow(width)
	if p.err != "" {
		return lipgloss.JoinVertical(lipgloss.Left,
			header,
			panelError(p.t, p.err, width, maxInt(1, height-lipgloss.Height(header))),
		)
	}

	tableHeight := maxInt(3, height-lipgloss.Height(header)-1)
	p.tbl.SetWidth(width)
	p.tbl.SetHeight(tableHeight)
	body := p.tbl.View()
	if len(p.snap.Activity) == 0 {
		body = panelEmpty(p.t, "no recent activity yet.", width, tableHeight)
	} else if p.entr != nil {
		body = applyFade(body, p.entr.activity.pos, p.t)
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, body)
}

// headerRow lays out banner (left, slides in from -bannerWidth to 0)
// and lockup + stats (right, slide in from +width). Stats columns are
// individual elements with staggered delays so the row "catches up"
// rather than entering as a single slab.
func (p *statusPanel) headerRow(width int) string {
	banner := renderWordmark(p.t)
	lockup := p.lockup()
	stats := p.statsBlock()

	bx := 0
	lx, sx := 0, 0
	l1off, l2off, l3off := 0, 0, 0
	if p.entr != nil {
		bx = offsetLeft(p.entr.banner)
		l1off = int(p.entr.lockup1.pos + 0.5)
		l2off = int(p.entr.lockup2.pos + 0.5)
		l3off = int(p.entr.lockup3.pos + 0.5)
		sx = int(p.entr.stats.pos + 0.5)
		_ = lx
	}

	bannerCol := slideHorizontal(banner, bx)
	lockupLines := strings.Split(lockup, "\n")
	if len(lockupLines) >= 1 {
		lockupLines[0] = padLeftLine(lockupLines[0], maxInt(0, l1off))
	}
	if len(lockupLines) >= 2 {
		lockupLines[1] = padLeftLine(lockupLines[1], maxInt(0, l2off))
	}
	if len(lockupLines) >= 3 {
		lockupLines[2] = padLeftLine(lockupLines[2], maxInt(0, l3off))
	}
	lockupCol := strings.Join(lockupLines, "\n")
	statsCol := slideHorizontal(stats, sx)

	bannerW := lipgloss.Width(bannerCol)
	statsW := lipgloss.Width(statsCol)
	lockupW := maxInt(0, width-bannerW-statsW-4)

	left := lipgloss.NewStyle().Width(bannerW).Render(bannerCol)
	mid := lipgloss.NewStyle().Width(lockupW).PaddingLeft(2).Render(lockupCol)
	right := lipgloss.NewStyle().Width(statsW).Render(statsCol)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, mid, right)
}

// slideHorizontal pads each line of s with leading spaces equal to
// max(0, offset). Used to hold an element off-screen during the
// entrance and slide it into place as the spring resolves.
func slideHorizontal(s string, offset int) string {
	if offset <= 0 {
		return s
	}
	return padLeft(s, offset)
}

// padLeftLine pads a single line; preserves embedded ANSI by treating
// the prefix as raw spaces (lipgloss measurement still works because
// the prefix is plain ASCII).
func padLeftLine(s string, n int) string {
	if n <= 0 {
		return s
	}
	return strings.Repeat(" ", n) + s
}

// applyFade dims the body during the activity-fade window. opacity in
// [0,1]; 0 = fully dim, 1 = full theme. We approximate alpha by
// switching between dim and normal styles based on a threshold — cheap
// and avoids per-character color blending.
func applyFade(body string, opacity float64, t *ui.Theme) string {
	if t == nil || !t.Enabled || opacity >= 1 {
		return body
	}
	if opacity <= 0 {
		return t.DimStyle.Render(body)
	}
	// At opacity 0..0.5 keep dim; 0.5..1 reveal.
	if opacity < 0.5 {
		return t.DimStyle.Render(body)
	}
	return body
}

func (p *statusPanel) lockup() string {
	v := p.snap.DriftVersion
	if v == "" {
		v = p.o.DriftVersion
	}
	bold := boldFor(p.t)
	dim := dimFor(p.t)
	return lipgloss.JoinVertical(lipgloss.Left,
		bold.Render(fmt.Sprintf("drift %s", v)),
		dim.Render("devpods for drifters"),
		dim.Render(""),
	)
}

func (p *statusPanel) statsBlock() string {
	bold := boldFor(p.t)
	dim := dimFor(p.t)
	rows := []struct{ num, label string }{
		{fmt.Sprintf("%d / %d", p.snap.CircuitsReachable, p.snap.CircuitsTotal), "circuits"},
		{fmt.Sprintf("%d / %d", p.snap.KartsRunning, p.snap.KartsTotal), "karts"},
		{fmt.Sprintf("%d", p.snap.PortsActive), "ports"},
	}
	numW := 0
	for _, r := range rows {
		if w := lipgloss.Width(r.num); w > numW {
			numW = w
		}
	}
	lines := make([]string, len(rows))
	for i, r := range rows {
		num := lipgloss.NewStyle().Width(numW).Align(lipgloss.Right).Render(bold.Render(r.num))
		lines[i] = lipgloss.JoinHorizontal(lipgloss.Top, num, "  ", dim.Render(r.label))
	}
	return lipgloss.NewStyle().Padding(0, 1).Render(lipgloss.JoinVertical(lipgloss.Right, lines...))
}

func activityRows(entries []ActivityEntry, now time.Time, t *ui.Theme) []table.Row {
	dim := dimFor(t)
	out := make([]table.Row, 0, len(entries))
	for _, e := range entries {
		kart := e.Kart
		if kart == "" {
			kart = "—"
		}
		out = append(out, table.Row{
			dim.Render(relTime(now, e.When)),
			e.Action,
			kart,
			dim.Render(e.Detail),
		})
	}
	return out
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
