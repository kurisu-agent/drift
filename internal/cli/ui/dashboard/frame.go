package dashboard

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// RenderSettledFrame builds the dashboard model with the given options,
// drains every cmd Init emits (data fetches, animation start), delivers
// a WindowSizeMsg so panels lay out, then snaps the entrance animation
// to its settled state and returns View().Content with full color ANSI.
// Pipe into `freeze` to produce a PNG still.
//
// Used by cmd/dashboard-frame for the visual eval loop. Headless: no
// TTY, no real-time, no goroutine leaks.
func RenderSettledFrame(o Options, width, height int) string {
	rm := buildFrameModel(o, width, height)
	if sp, ok := rm.panels[TabStatus].(*statusPanel); ok && sp.entr != nil {
		sp.entr.settleNow()
	}
	return rm.View().Content
}

// RenderFrameAt is RenderSettledFrame's mid-animation cousin: it
// captures the dashboard at simulated time `at` measured from the
// entrance start. The springs are stepped frame-by-frame at 60 FPS
// up to `at`, ignoring wall clock so the captured frame is
// deterministic regardless of how long the host took to produce it.
//
// Use 0ms for the very first frame (everything off-screen), then
// increment by 1/60 (~16ms) for each "tick in" the caller wants
// to inspect.
func RenderFrameAt(o Options, width, height int, at time.Duration) string {
	// Force MotionDisabled off so the entrance runs through the spring
	// path even if a caller's Options had it on (the renderer is meant
	// to capture animation frames; settling early defeats the purpose).
	o.MotionDisabled = false
	rm := buildFrameModel(o, width, height)
	sp, ok := rm.panels[TabStatus].(*statusPanel)
	if !ok || sp.entr == nil {
		return rm.View().Content
	}
	const dt = time.Second / 60
	for elapsed := dt; elapsed <= at; elapsed += dt {
		if !sp.entr.advance(elapsed) {
			break
		}
	}
	return rm.View().Content
}

// buildFrameModel is the shared setup the settled and per-frame
// renderers both rely on: build the model, drain Init's cmds, deliver
// WindowSizeMsg so panels construct their entrance state.
func buildFrameModel(o Options, width, height int) *model {
	root := newModel(o)
	cmd := root.Init()
	deliverFrame(root, cmd)
	out, _ := root.Update(tea.WindowSizeMsg{Width: width, Height: height})
	rm, _ := out.(*model)
	return rm
}

// deliverFrame is the headless equivalent of bubbletea's runtime: pump
// cmds into Update until the queue drains. Per-cmd timeout caps a stuck
// goroutine; tickMsg (10s refresh) and animFrameMsg are skipped here so
// the caller controls when those fire.
func deliverFrame(m *model, cmd tea.Cmd) {
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		msg := runFrameCmd(c, 200*time.Millisecond)
		if msg == nil {
			continue
		}
		switch msg.(type) {
		case tickMsg, animFrameMsg:
			continue
		}
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, sub := range batch {
				queue = append(queue, sub)
			}
			continue
		}
		_, follow := m.Update(msg)
		if follow != nil {
			queue = append(queue, follow)
		}
	}
}

func runFrameCmd(c tea.Cmd, d time.Duration) tea.Msg {
	ch := make(chan tea.Msg, 1)
	go func() { ch <- c() }()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(d):
		return nil
	}
}
