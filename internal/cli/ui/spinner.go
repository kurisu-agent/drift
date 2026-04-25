package ui

import (
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"charm.land/bubbles/v2/spinner"
)

// showTimerAfter hides the elapsed-time suffix until the op has been
// running for this long. Short operations shouldn't look slow.
const showTimerAfter = 10 * time.Second

// SpinnerOptions controls a single spinner phase.
type SpinnerOptions struct {
	Message   string
	Transport string  // optional: rendered as " via <transport>" dim suffix.
	Frames    []string // override the default frame set (defaults to spinner.Dot).
	FPS       time.Duration
}

// Spinner is a single foreground operation rendered as a one-line
// spinner that resolves to a Success or Fail line. No-op when the
// owning theme is disabled (JSON / NO_COLOR / non-TTY) — the lifecycle
// methods still print a final line so call sites can stay unconditional.
type Spinner struct {
	w         io.Writer
	t         *Theme
	message   string
	transport string
	start     time.Time

	frames []string
	fps    time.Duration

	enabled bool
	stopped atomic.Bool

	mu     sync.Mutex
	idx    int
	stopCh chan struct{}
	done   sync.WaitGroup
}

// NewSpinner starts a spinner on w. Returns a non-nil Spinner even when
// disabled — Succeed/Fail still emit a plain text final line.
func (t *Theme) NewSpinner(w io.Writer, o SpinnerOptions) *Spinner {
	frames := o.Frames
	if len(frames) == 0 {
		frames = spinner.Dot.Frames
	}
	fps := o.FPS
	if fps == 0 {
		fps = spinner.Dot.FPS
	}
	sp := &Spinner{
		w:         w,
		t:         t,
		message:   o.Message,
		transport: o.Transport,
		start:     time.Now(),
		frames:    frames,
		fps:       fps,
		enabled:   t != nil && t.Enabled,
	}
	if !sp.enabled {
		return sp
	}
	sp.stopCh = make(chan struct{})
	sp.done.Add(1)
	go sp.run()
	return sp
}

func (s *Spinner) run() {
	defer s.done.Done()
	tk := time.NewTicker(s.fps)
	defer tk.Stop()
	s.draw()
	for {
		select {
		case <-s.stopCh:
			return
		case <-tk.C:
			s.mu.Lock()
			s.idx = (s.idx + 1) % len(s.frames)
			s.mu.Unlock()
			s.draw()
		}
	}
}

func (s *Spinner) draw() {
	s.mu.Lock()
	frame := s.frames[s.idx]
	s.mu.Unlock()
	suffix := s.message
	if s.transport != "" {
		suffix += " " + s.t.Dim("via "+s.transport)
	}
	if elapsed := time.Since(s.start); elapsed >= showTimerAfter {
		suffix += " " + s.t.Dim(fmtDuration(elapsed))
	}
	fmt.Fprintf(s.w, "\r\x1b[K%s %s", s.t.Accent(frame), suffix)
}

// Succeed stops the spinner and prints "✓ msg" green.
func (s *Spinner) Succeed(finalMsg string) {
	if s == nil || s.stopped.Swap(true) {
		return
	}
	s.stop()
	if !s.enabled {
		fmt.Fprintln(s.w, finalMsg)
		return
	}
	fmt.Fprintf(s.w, "%s %s\n", s.t.Success(Icon(IconSuccess)), finalMsg)
}

// Fail stops the spinner and prints "✗ <msg> failed" red.
func (s *Spinner) Fail() {
	if s == nil || s.stopped.Swap(true) {
		return
	}
	s.stop()
	if !s.enabled {
		return
	}
	fmt.Fprintf(s.w, "%s %s failed\n", s.t.Error(Icon(IconError)), s.message)
}

// Stop halts the spinner with no final line. The caller will print the
// final state itself.
func (s *Spinner) Stop() {
	if s == nil || s.stopped.Swap(true) {
		return
	}
	s.stop()
}

func (s *Spinner) stop() {
	if s.stopCh != nil {
		close(s.stopCh)
		s.done.Wait()
		s.stopCh = nil
	}
	if s.enabled {
		fmt.Fprint(s.w, "\r\x1b[K")
	}
}

func fmtDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d / time.Second)
	m := total / 60
	sec := total % 60
	return fmt.Sprintf("%d:%02d", m, sec)
}
