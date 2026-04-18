package devpod

import "strings"

// Status is the devpod workspace lifecycle state, normalized to the small
// enum lakitu exposes over the wire:
// `running|stopped|busy|error|not_found`. devpod itself reports slightly
// more states ("Running", "Stopped", "Busy", "NotFound") so the wrapper
// maps them deterministically.
type Status string

const (
	// StatusRunning means the container is up and reachable.
	StatusRunning Status = "running"
	// StatusStopped means the container exists but is not running.
	StatusStopped Status = "stopped"
	// StatusBusy covers transitional states: starting, stopping,
	// provisioning. Treat as "try again in a moment".
	StatusBusy Status = "busy"
	// StatusError is devpod reporting a terminal failure state for the
	// workspace. Callers should surface this to the user; the garage may
	// still be recoverable via `devpod delete` + `drift new`.
	StatusError Status = "error"
	// StatusNotFound means devpod does not know about this workspace.
	// From lakitu's side, this combined with a populated garage entry is
	// the signal for a stale kart.
	StatusNotFound Status = "not_found"
)

// normalizeStatus folds devpod's raw status string into the enum above.
// Unknown states map to StatusError so the kart surfaces rather than
// silently masquerading as something it isn't.
func normalizeStatus(raw string) Status {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "running":
		return StatusRunning
	case "stopped":
		return StatusStopped
	case "busy", "starting", "stopping":
		return StatusBusy
	case "notfound", "not_found", "":
		return StatusNotFound
	default:
		return StatusError
	}
}
