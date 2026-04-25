package ui

import (
	"fmt"
	"io"
	"sync/atomic"

	"charm.land/bubbles/v2/progress"
)

// ProgressBar is a determinate progress bar driven from a non-tea caller
// (drift update, kart rebuild). Backed by bubbles/v2/progress for
// rendering; we drive ViewAs() ourselves on each Set() update.
type ProgressBar struct {
	w     io.Writer
	t     *Theme
	model progress.Model
	width int
	label string

	enabled bool
	stopped atomic.Bool
	current float64
}

// ProgressOptions configures a ProgressBar.
type ProgressOptions struct {
	Width int    // total width (default 40)
	Label string // prefix, e.g. "downloading"
}

// NewProgress builds a determinate progress bar. The bar is no-op when
// the theme is disabled (JSON / non-TTY).
func (t *Theme) NewProgress(w io.Writer, o ProgressOptions) *ProgressBar {
	width := o.Width
	if width <= 0 {
		width = 40
	}
	pb := &ProgressBar{
		w:       w,
		t:       t,
		width:   width,
		label:   o.Label,
		enabled: t != nil && t.Enabled,
	}
	if pb.enabled {
		pb.model = progress.New(progress.WithDefaultBlend(), progress.WithWidth(width))
	}
	return pb
}

// Set updates the bar's fill ratio (0..1) and redraws.
func (pb *ProgressBar) Set(ratio float64) {
	if pb == nil || pb.stopped.Load() {
		return
	}
	if ratio < 0 {
		ratio = 0
	} else if ratio > 1 {
		ratio = 1
	}
	pb.current = ratio
	if !pb.enabled {
		return
	}
	rendered := pb.model.ViewAs(ratio)
	prefix := ""
	if pb.label != "" {
		prefix = pb.t.Dim(pb.label) + " "
	}
	fmt.Fprintf(pb.w, "\r\x1b[K%s%s", prefix, rendered)
}

// SetLabel changes the leading label and redraws at the current ratio.
func (pb *ProgressBar) SetLabel(label string) {
	pb.label = label
	pb.Set(pb.current)
}

// Done clears the bar.
func (pb *ProgressBar) Done() {
	if pb == nil || pb.stopped.Swap(true) {
		return
	}
	if pb.enabled {
		fmt.Fprint(pb.w, "\r\x1b[K")
	}
}

// SetWidth adjusts the visible width of the bar.
func (pb *ProgressBar) SetWidth(w int) {
	if w <= 0 {
		return
	}
	pb.width = w
	if pb.enabled {
		pb.model = progress.New(progress.WithDefaultBlend(), progress.WithWidth(w))
	}
}
