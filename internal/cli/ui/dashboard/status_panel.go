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
	"github.com/charmbracelet/x/ansi"
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
	// Kick off the data fetch and the entrance frame loop in parallel.
	// The entrance object itself is built lazily on the first
	// WindowSizeMsg — its spring targets are width-relative.
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

// headerRow lays out the wordmark (left, bouncing in from off-screen
// left), the lockup (middle, sliding in from the right), and the stats
// column (right, sliding in from the right). The banner's slot is a
// fixed bannerWidth columns wide so its overshoot doesn't reflow the
// surrounding layout — the wordmark is clipped or padded inside the
// slot via renderBannerSliding.
func (p *statusPanel) headerRow(width int) string {
	gradient := wordmarkGradient(p.t)
	lockup := p.lockup()
	stats := p.statsBlock()

	// Natural widths so the columns don't grow when slide offsets
	// push content past the right edge — the Width()/MaxWidth() pair
	// below clips overflow per line instead of letting it wrap.
	bannerW := bannerWidth
	statsW := lipgloss.Width(stats)
	lockupW := maxInt(0, width-bannerW-statsW-4)

	bx := 0
	l1off, l2off, l3off, sx := 0, 0, 0, 0
	if p.entr != nil {
		bx = offsetLeft(p.entr.banner)
		// Cap slide offsets at the column width so a far-off-screen
		// position doesn't blow the row past the screen width when
		// rendered. Once content slides into the column the cap is
		// redundant; before that, padding fills the column with
		// spaces (content invisible) which is the desired effect.
		l1off = clampInt(int(p.entr.lockup1.pos+0.5), 0, lockupW)
		l2off = clampInt(int(p.entr.lockup2.pos+0.5), 0, lockupW)
		l3off = clampInt(int(p.entr.lockup3.pos+0.5), 0, lockupW)
		sx = clampInt(int(p.entr.stats.pos+0.5), 0, statsW)
	}

	bannerCol := renderBannerSliding(wordmark, gradient, bx, bannerWidth)
	lockupCol := clipColumn(lockup, []int{l1off, l2off, l3off}, lockupW)
	statsCol := clipColumn(stats, repeat(sx, lipgloss.Height(stats)), statsW)

	// 2-col gutter between banner and lockup, no border. lipgloss.JoinHorizontal
	// is height-respecting; every column is the same height (3 rows) so no
	// wrap-induced row inflation can happen here.
	gutter := strings.Repeat(" ", 2)
	rows := joinHorizontalRows(bannerCol, gutter, lockupCol, gutter, statsCol)
	return rows
}

// clipColumn pads each line of `s` by the matching offset and then
// truncates the line to `width` columns using ansi-aware truncation.
// This guarantees the returned block is exactly `width` columns wide
// regardless of the slide offset, so JoinHorizontal can't reflow rows.
func clipColumn(s string, offsets []int, width int) string {
	if width <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	out := make([]string, len(lines))
	for i, line := range lines {
		off := 0
		if i < len(offsets) {
			off = offsets[i]
		}
		padded := padLeftLine(line, off)
		clipped := ansi.Truncate(padded, width, "")
		// Pad the clipped line back out to width with trailing spaces
		// so every row of the column has identical visible width.
		visW := lipgloss.Width(clipped)
		if visW < width {
			clipped += strings.Repeat(" ", width-visW)
		}
		out[i] = clipped
	}
	return strings.Join(out, "\n")
}

// joinHorizontalRows pairs each row of every column and concatenates
// them with the supplied separator. lipgloss.JoinHorizontal does this
// too but pads short columns with extra rows; clipColumn already
// guarantees equal heights so we can stay simple here and avoid that
// path entirely.
func joinHorizontalRows(cols ...string) string {
	splits := make([][]string, len(cols))
	maxRows := 0
	for i, c := range cols {
		splits[i] = strings.Split(c, "\n")
		if n := len(splits[i]); n > maxRows {
			maxRows = n
		}
	}
	rows := make([]string, maxRows)
	for r := 0; r < maxRows; r++ {
		var b strings.Builder
		for _, lines := range splits {
			if r < len(lines) {
				b.WriteString(lines[r])
			}
		}
		rows[r] = b.String()
	}
	return strings.Join(rows, "\n")
}

func repeat(v, n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = v
	}
	return out
}

// clampInt confines v to [lo, hi]. Used by the entrance to keep slide
// offsets within their owning column so wrap doesn't break the row.
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
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
