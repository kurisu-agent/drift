package ui

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// PhaseTracker renders a multi-phase operation as a single-line spinner
// "[N/M] <current phase>...", collapsing each completed phase to a dim
// checkmark. Used by drift new (clone, up, dotfiles, finalize) and drift
// kart rebuild. No-op when the theme is disabled.
type PhaseTracker struct {
	w       io.Writer
	t       *Theme
	phases  []string
	enabled bool

	mu      sync.Mutex
	current int
	start   time.Time
	spin    *Spinner
}

// NewPhaseTracker builds a tracker for the given phase names. Phase index 0
// becomes active on first Advance/Begin. Pass nil for phases when the
// caller will supply names dynamically via Begin.
func (t *Theme) NewPhaseTracker(w io.Writer, phases []string) *PhaseTracker {
	pt := &PhaseTracker{
		w:       w,
		t:       t,
		phases:  append([]string(nil), phases...),
		current: -1,
		enabled: t != nil && t.Enabled,
	}
	return pt
}

// Begin advances to the named phase. The previous phase (if any) is
// printed as a dim checkmark line above the new spinner.
func (pt *PhaseTracker) Begin(name string) {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	if pt.spin != nil {
		pt.spin.Stop()
		pt.printDoneLine(pt.phases[pt.current])
		pt.spin = nil
	}

	pt.current++
	if pt.current >= len(pt.phases) {
		pt.phases = append(pt.phases, name)
	} else {
		pt.phases[pt.current] = name
	}
	pt.start = time.Now()

	pt.spin = pt.t.NewSpinner(pt.w, SpinnerOptions{
		Message: pt.label(),
	})
}

// Done finishes the current phase as a success and clears the spinner.
// finalMsg, if non-empty, is appended to the dim done line.
func (pt *PhaseTracker) Done(finalMsg string) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	if pt.spin == nil {
		return
	}
	pt.spin.Stop()
	pt.spin = nil
	if pt.current >= 0 && pt.current < len(pt.phases) {
		label := pt.phases[pt.current]
		if finalMsg != "" {
			label = finalMsg
		}
		pt.printDoneLine(label)
	}
}

// Fail marks the current phase as failed and clears the spinner.
func (pt *PhaseTracker) Fail() {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	if pt.spin == nil {
		return
	}
	pt.spin.Stop()
	pt.spin = nil
	if pt.current >= 0 && pt.current < len(pt.phases) && pt.enabled {
		fmt.Fprintf(pt.w, "%s %s failed\n", pt.t.Error(Icon(IconError)), pt.phases[pt.current])
	}
}

func (pt *PhaseTracker) printDoneLine(name string) {
	if !pt.enabled {
		fmt.Fprintln(pt.w, name)
		return
	}
	check := pt.t.Success(Icon(IconSuccess))
	fmt.Fprintf(pt.w, "%s %s\n", check, pt.t.Dim(name))
}

func (pt *PhaseTracker) label() string {
	total := len(pt.phases)
	if total == 0 {
		return ""
	}
	return fmt.Sprintf("[%d/%d] %s", pt.current+1, total, pt.phases[pt.current])
}
