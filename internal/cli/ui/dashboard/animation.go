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
// The status banner slides in from the left, the lockup follows from
// the right, then the stats column, then the activity table fades in.
//
// All four use the same harmonica spring (frequency 6.0, damping 0.5)
// so the motion feels like one mechanical system. Per-piece delays
// stagger the start so the eye reads them as a sequence, not a block.
type entrance struct {
	spring  harmonica.Spring
	started time.Time
	done    bool
	skipped bool

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
		spring:  harmonica.NewSpring(harmonica.FPS(60), 6.0, 0.5),
		started: time.Now(),
	}
	skip := motionDisabled || os.Getenv("DRIFT_NO_MOTION") != "" ||
		width < bannerWidth+12 || os.Getenv("GO_TEST_DETERMINISTIC") != ""
	// Banner: slides in from -bannerWidth to 0.
	e.banner = element{start: float64(-bannerWidth), pos: float64(-bannerWidth), target: 0, delay: 0}
	// Lockup lines: slide in from +width to 0 (offset from final col).
	for i, dst := range []*element{&e.lockup1, &e.lockup2, &e.lockup3} {
		*dst = element{
			start:  float64(width),
			pos:    float64(width),
			target: 0,
			delay:  time.Duration(150+50*i) * time.Millisecond,
		}
	}
	// Stats: slides in from +width.
	e.stats = element{
		start:  float64(width),
		pos:    float64(width),
		target: 0,
		delay:  250 * time.Millisecond,
	}
	// Activity table: fade 0..1 starting after the others have begun.
	e.activity = element{start: 0, pos: 0, target: 1, delay: 300 * time.Millisecond}

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

// tick advances every spring whose delay has elapsed. Returns true
// when at least one element is still moving — caller schedules another
// frame in that case.
func (e *entrance) tick() bool {
	if e.done {
		return false
	}
	elapsed := time.Since(e.started)
	advance := func(el *element) {
		if elapsed < el.delay {
			return
		}
		el.pos, el.vel = e.spring.Update(el.pos, el.vel, el.target)
	}
	advance(&e.banner)
	advance(&e.lockup1)
	advance(&e.lockup2)
	advance(&e.lockup3)
	advance(&e.stats)
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
func offsetLeft(el element) int { return int(el.pos + 0.5) }

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
