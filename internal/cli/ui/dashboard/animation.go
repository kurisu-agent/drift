package dashboard

import (
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/harmonica"
)

// animFrameMsg is sent by the per-frame ticker while the entrance
// animation is running. Status panel consumes it; root model forwards.
type animFrameMsg time.Time

//nolint:unused // kept for the parked entrance animation; see status_panel.Init.
func animFrameCmd() tea.Cmd {
	return tea.Tick(time.Second/60, func(t time.Time) tea.Msg { return animFrameMsg(t) })
}

// element is one animated piece of the status panel header. Each piece
// has a current x position (in cells), a velocity, a target, and a
// startDelay measured from animation start. Until elapsed >= startDelay
// the spring isn't advanced — the piece sits at its initial offscreen
// position.
type element struct {
	pos, vel, start, target float64
	delay                   time.Duration
}

// settled reports whether the spring has effectively reached its
// target. Both position and velocity must be near-zero in delta terms.
//
//nolint:unused // kept for the parked entrance animation.
func (e element) settled() bool {
	const eps = 0.5
	dx := e.target - e.pos
	if dx < 0 {
		dx = -dx
	}
	v := e.vel
	if v < 0 {
		v = -v
	}
	return dx < eps && v < eps
}

// entrance owns the spring state for the status panel's first paint.
// The wordmark bounces in from the left first (low-damped spring so it
// overshoots and settles), then the lockup lines slide in from the
// right, then the stats column, then the activity table fades in.
//
// The banner uses a snappier, bouncier spring; the lockup/stats use a
// gentler one so the staggered entrance reads as one mechanical system
// even though the parts have different feels.
type entrance struct {
	bannerSpring harmonica.Spring
	textSpring   harmonica.Spring
	started      time.Time
	done         bool
	skipped      bool

	banner   element
	lockup1  element
	lockup2  element
	lockup3  element
	stats    element
	activity element // 0..1 fade
}

// newEntrance builds an entrance state for a status panel laid out in
// `width` columns. opt-out paths (DRIFT_NO_MOTION, --no-motion,
// disabled theme, narrow terminal, test runs) jump straight to the
// settled state.
func newEntrance(width int, motionDisabled bool) *entrance {
	e := &entrance{
		// Banner spring: low damping so the wordmark overshoots and
		// settles back — that's the visible "bounce".
		bannerSpring: harmonica.NewSpring(harmonica.FPS(60), 7.0, 0.35),
		// Text spring: moderate damping so the lockup / stats glide in
		// without bouncing, contrasting with the banner.
		textSpring: harmonica.NewSpring(harmonica.FPS(60), 6.0, 0.7),
		started:    time.Now(),
	}
	skip := motionDisabled || os.Getenv("DRIFT_NO_MOTION") != "" ||
		width < bannerWidth+12 || os.Getenv("GO_TEST_DETERMINISTIC") != ""
	// Banner: starts off-screen left (pos = -2 * bannerWidth gives the
	// spring some travel to build velocity before reaching col 0), and
	// the bouncy spring overshoots target=0 once or twice before
	// settling. delay=0 — banner enters first.
	e.banner = element{start: float64(-2 * bannerWidth), pos: float64(-2 * bannerWidth), target: 0, delay: 0}
	// Lockup lines: slide in from the right edge of the panel after
	// the banner has had time to land. 50ms cascade keeps lines from
	// arriving in lockstep.
	for i, dst := range []*element{&e.lockup1, &e.lockup2, &e.lockup3} {
		*dst = element{
			start:  float64(width),
			pos:    float64(width),
			target: 0,
			delay:  time.Duration(420+60*i) * time.Millisecond,
		}
	}
	// Stats: arrives last among the sliding pieces.
	e.stats = element{
		start:  float64(width),
		pos:    float64(width),
		target: 0,
		delay:  600 * time.Millisecond,
	}
	// Activity table: fade 0..1 starting after the slides have begun.
	e.activity = element{start: 0, pos: 0, target: 1, delay: 700 * time.Millisecond}

	if skip {
		e.banner.pos, e.banner.vel = e.banner.target, 0
		e.lockup1.pos, e.lockup1.vel = e.lockup1.target, 0
		e.lockup2.pos, e.lockup2.vel = e.lockup2.target, 0
		e.lockup3.pos, e.lockup3.vel = e.lockup3.target, 0
		e.stats.pos, e.stats.vel = e.stats.target, 0
		e.activity.pos, e.activity.vel = e.activity.target, 0
		e.done = true
		e.skipped = true
	}
	return e
}

// settleNow snaps every element to its target with zero velocity and
// marks the entrance as done. Used by the headless frame renderer so
// settled-state PNGs don't depend on wall-clock progressing inside a
// for-loop while the spring's per-element delays are wall-clock gated.
func (e *entrance) settleNow() {
	if e == nil {
		return
	}
	for _, el := range []*element{&e.banner, &e.lockup1, &e.lockup2, &e.lockup3, &e.stats} {
		el.pos, el.vel = el.target, 0
	}
	e.activity.pos = 1
	e.done = true
}

// tick advances every spring whose delay has elapsed. Returns true
// when at least one element is still moving — caller schedules another
// frame in that case.
//
//nolint:unused // kept for the parked entrance animation.
func (e *entrance) tick() bool {
	if e.done {
		return false
	}
	elapsed := time.Since(e.started)
	advance := func(el *element, sp harmonica.Spring) {
		if elapsed < el.delay {
			return
		}
		el.pos, el.vel = sp.Update(el.pos, el.vel, el.target)
	}
	advance(&e.banner, e.bannerSpring)
	advance(&e.lockup1, e.textSpring)
	advance(&e.lockup2, e.textSpring)
	advance(&e.lockup3, e.textSpring)
	advance(&e.stats, e.textSpring)
	// Activity uses linear interp on a 0..1 axis with the same delay
	// gating, so it shares the per-frame loop without a second cmd.
	if elapsed >= e.activity.delay {
		const fadeMS = 300.0
		t := float64(elapsed-e.activity.delay) / (fadeMS * float64(time.Millisecond))
		if t > 1 {
			t = 1
		}
		e.activity.pos = t
	}
	if e.banner.settled() && e.lockup1.settled() && e.lockup2.settled() &&
		e.lockup3.settled() && e.stats.settled() && e.activity.pos >= 1 {
		e.done = true
		return false
	}
	return true
}

// offsetLeft returns the integer column offset for an element sliding
// in from the left. Positions are floats; we round to whole cells per
// the plan's "no sub-pixel shimmer" rule.
func offsetLeft(el element) int {
	x := el.pos
	if x < 0 {
		return -int(-x + 0.5)
	}
	return int(x + 0.5)
}

// padLeft pads s with `n` leading spaces (clamped at 0). Used to slide
// content rightward during entrance.
func padLeft(s string, n int) string {
	if n <= 0 {
		return s
	}
	pad := strings.Repeat(" ", n)
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = pad + line
	}
	return strings.Join(lines, "\n")
}

// renderBannerSliding returns a slotWidth-wide block where each line of
// `banner` is positioned with its left edge at column `x` (relative to
// the slot's left edge). Negative x clips the wordmark from the left
// (it's emerging from off-screen); positive x pads with leading spaces
// (the spring has overshot col 0 during the bounce). The output is
// always exactly slotWidth columns per line so the surrounding layout
// doesn't reflow as the banner moves.
func renderBannerSliding(banner string, x, slotWidth int) string {
	lines := strings.Split(banner, "\n")
	out := make([]string, len(lines))
	for i, line := range lines {
		runes := []rune(line)
		out[i] = sliceLineAtX(runes, x, slotWidth)
	}
	return strings.Join(out, "\n")
}

func sliceLineAtX(runes []rune, x, slotWidth int) string {
	if slotWidth <= 0 {
		return ""
	}
	if x >= slotWidth {
		return strings.Repeat(" ", slotWidth)
	}
	leadingPad := 0
	visible := runes
	if x < 0 {
		skip := -x
		if skip >= len(visible) {
			return strings.Repeat(" ", slotWidth)
		}
		visible = visible[skip:]
	} else {
		leadingPad = x
	}
	if leadingPad+len(visible) > slotWidth {
		visible = visible[:slotWidth-leadingPad]
	}
	trailing := slotWidth - leadingPad - len(visible)
	if trailing < 0 {
		trailing = 0
	}
	return strings.Repeat(" ", leadingPad) + string(visible) + strings.Repeat(" ", trailing)
}
