// Package progress wraps briandowns/spinner with drift's style + no-op
// discipline. A Phase is the lifetime of one user-visible operation:
// `creating kart "alpha" via ssh…` rendered to stderr, replaced on Succeed
// or Fail with a final line. No-op under --output json or a non-TTY
// writer so CI logs and `drift new ... | jq` stay clean.
package progress

import (
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/briandowns/spinner"
	"github.com/kurisu-agent/drift/internal/cli/ui"
)

// showTimerAfter hides the elapsed-time suffix until the op has been
// running for this long — short operations shouldn't look slow.
const showTimerAfter = 10 * time.Second

type Phase struct {
	w         io.Writer
	p         *ui.Theme
	message   string
	transport string
	start     time.Time

	spinner *spinner.Spinner

	timerStop chan struct{}
	timerDone sync.WaitGroup

	// enabled mirrors palette.Enabled at construction so callers still get
	// a non-nil Phase in disabled mode (Succeed/Fail just print a plain line).
	enabled bool
	stopped atomic.Bool
}

// Start prints a spinner to w with "message via <transport>…" suffix.
// Pass transport == "" to suppress the hint. jsonMode and non-TTY writers
// short-circuit to a no-op Phase — the returned pointer is always usable.
func Start(w io.Writer, jsonMode bool, message, transport string) *Phase {
	p := ui.NewTheme(w, jsonMode)
	ph := &Phase{
		w:         w,
		p:         p,
		message:   message,
		transport: transport,
		start:     time.Now(),
		enabled:   p.Enabled,
	}
	if !ph.enabled {
		return ph
	}
	s := spinner.New(spinner.CharSets[14], 100*time.Millisecond,
		spinner.WithWriter(w),
		spinner.WithHiddenCursor(true),
	)
	s.Suffix = " " + ph.suffix(0)
	s.Start()
	ph.spinner = s
	ph.timerStop = make(chan struct{})
	ph.timerDone.Add(1)
	go ph.runTimer()
	return ph
}

func (ph *Phase) suffix(elapsed time.Duration) string {
	s := ph.message
	if ph.transport != "" {
		s += " " + ph.p.Dim("via "+ph.transport)
	}
	if elapsed >= showTimerAfter {
		s += " " + ph.p.Dim(fmtDuration(elapsed))
	}
	return s
}

// runTimer refreshes the spinner suffix each second so a long-running op
// shows an elapsed counter past 10s. The first 10s are a no-op — suffix()
// renders the same string while elapsed < showTimerAfter — so we sleep
// until the threshold before starting the per-second ticker. Exits
// cleanly when Stop/Succeed/Fail close timerStop.
func (ph *Phase) runTimer() {
	defer ph.timerDone.Done()
	timer := time.NewTimer(showTimerAfter)
	defer timer.Stop()
	select {
	case <-ph.timerStop:
		return
	case <-timer.C:
	}
	tk := time.NewTicker(time.Second)
	defer tk.Stop()
	ph.refreshSuffix()
	for {
		select {
		case <-ph.timerStop:
			return
		case <-tk.C:
			ph.refreshSuffix()
		}
	}
}

// refreshSuffix rewrites the spinner's suffix with the current elapsed
// time. Returns early if the spinner has already been torn down — a race
// the previous code handled inline.
func (ph *Phase) refreshSuffix() {
	if ph.spinner == nil {
		return
	}
	elapsed := time.Since(ph.start)
	ph.spinner.Lock()
	ph.spinner.Suffix = " " + ph.suffix(elapsed)
	ph.spinner.Unlock()
}

// Succeed clears the spinner and prints a green "✓ <finalMsg>" line. When
// disabled, writes the plain finalMsg instead.
func (ph *Phase) Succeed(finalMsg string) {
	if ph == nil || ph.stopped.Swap(true) {
		return
	}
	ph.stopSpinner()
	if !ph.enabled {
		fmt.Fprintln(ph.w, finalMsg)
		return
	}
	fmt.Fprintf(ph.w, "%s %s\n", ph.p.Success("✓"), finalMsg)
}

// Fail clears the spinner and prints a red "✗ <message> failed" line. The
// caller then renders the underlying error (typically through errfmt).
func (ph *Phase) Fail() {
	if ph == nil || ph.stopped.Swap(true) {
		return
	}
	ph.stopSpinner()
	if !ph.enabled {
		return
	}
	fmt.Fprintf(ph.w, "%s %s failed\n", ph.p.Error("✗"), ph.message)
}

// Stop halts the spinner with no final line. Useful when the caller will
// hand off to another renderer that owns the final output.
func (ph *Phase) Stop() {
	if ph == nil || ph.stopped.Swap(true) {
		return
	}
	ph.stopSpinner()
}

func (ph *Phase) stopSpinner() {
	if ph.timerStop != nil {
		close(ph.timerStop)
		ph.timerDone.Wait()
		ph.timerStop = nil
	}
	if ph.spinner != nil {
		ph.spinner.Stop()
		ph.spinner = nil
	}
}

// fmtDuration renders m:ss for the spinner timer suffix. Minutes-only is
// sufficient since anything past ~10s is already slow.
func fmtDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d / time.Second)
	m := total / 60
	s := total % 60
	return fmt.Sprintf("%d:%02d", m, s)
}
