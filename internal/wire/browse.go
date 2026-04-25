package wire

// CircuitBrowseStartParams asks lakitu to spawn (or reuse) a
// filebrowser process on the circuit, rooted at the drift workspaces
// tree so every kart's source shows up in one browser tab. The
// workstation forwards a localhost port to the returned remote port
// over its existing ssh connection to the circuit.
//
// Empty params on purpose — the root and bind address are server
// policy, not client choice. Letting the client pick the root would
// be a subtle local-file-disclosure footgun (e.g. `--root /etc`).
type CircuitBrowseStartParams struct{}

// CircuitBrowseStartResult reports the localhost port filebrowser is
// listening on (always 127.0.0.1) and the absolute filesystem path it
// is rooted at, so the workstation can render a useful "browsing X"
// status line. AlreadyRunning is true when this RPC reused a process
// that was already up — letting the client distinguish a fresh start
// from a re-attach without an extra round-trip.
type CircuitBrowseStartResult struct {
	Port           int    `json:"port"`
	Root           string `json:"root"`
	AlreadyRunning bool   `json:"already_running,omitempty"`
}

// CircuitBrowseStopParams asks lakitu to kill the running filebrowser
// process. Idempotent: stopping a non-running browser is a no-op.
type CircuitBrowseStopParams struct{}

// CircuitBrowseStopResult reports whether a running process was killed
// (Stopped=true) or there was nothing to stop (Stopped=false). The
// drift client uses this to phrase its closing status line.
type CircuitBrowseStopResult struct {
	Stopped bool `json:"stopped"`
}
