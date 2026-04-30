package devpod

import "strings"

// Status is the normalized lifecycle enum lakitu exposes over the wire.
// devpod itself reports more states ("Running", "Stopped", "Busy",
// "NotFound") which normalizeStatus folds in.
type Status string

const (
	StatusRunning Status = "running"
	StatusStopped Status = "stopped"
	// StatusBusy: starting, stopping, provisioning — treat as "retry soon".
	StatusBusy Status = "busy"
	// StatusError: terminal failure per devpod. Garage may still be
	// recoverable via `devpod delete` + `drift new`.
	StatusError Status = "error"
	// StatusNotFound + a populated garage entry = stale kart.
	StatusNotFound Status = "not_found"
)

// normalizeStatus maps unknown states to StatusError so they surface
// rather than masquerading.
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

// FromDockerState maps a docker container State onto the same lifecycle
// enum `devpod status` returns, so lakitu can fold a docker-batch lookup
// and per-workspace devpod probes into one shape. Empty input means "no
// container with this UID" — devpod treats that as stopped (the workspace
// can be re-upped), so we mirror that.
func FromDockerState(state string) Status {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "running":
		return StatusRunning
	case "restarting", "removing":
		return StatusBusy
	case "paused", "exited", "created", "dead", "":
		return StatusStopped
	default:
		return StatusError
	}
}
