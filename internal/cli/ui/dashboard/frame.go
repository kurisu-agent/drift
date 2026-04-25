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
// TTY, no real-time, no goroutine leaks. Settling synchronously avoids
// the wall-clock gating inside entrance.tick — a fast-spinning loop
// would race past the per-element delays without ever advancing the
// later springs.
func RenderSettledFrame(o Options, width, height int) string {
	root := newModel(o)

	cmd := root.Init()
	deliverFrame(root, cmd)

	out, _ := root.Update(tea.WindowSizeMsg{Width: width, Height: height})
	rm, _ := out.(*model)

	if sp, ok := rm.panels[TabStatus].(*statusPanel); ok && sp.entr != nil {
		sp.entr.settleNow()
	}
	return rm.View().Content
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
