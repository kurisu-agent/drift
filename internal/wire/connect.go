package wire

// KartConnectParams asks lakitu to build the exact remote command the
// client should pass to `ssh -t` / `mosh --` to attach to the kart.
// Name is the kart to connect to. Stdio asks for a stdio-tunneling
// variant (`devpod ssh --stdio`) suitable for use as an SSH
// ProxyCommand (e.g. drift's `Host drift.*.*` wildcard alias) — needed
// so callers can `ssh drift.<circuit>.<kart>` directly without
// rebuilding the env prefix on the workstation.
type KartConnectParams struct {
	Name  string `json:"name"`
	Stdio bool   `json:"stdio,omitempty"`
}

// KartConnectResult: Argv is the remote-command token sequence the client
// hands to the transport verbatim. Lakitu bakes the devpod binary path,
// the DEVPOD_HOME env prefix, and any kart-scoped `--set-env KEY=VALUE`
// pairs into this argv so the client never has to:
//   - know where the drift-managed devpod lives on the circuit,
//   - replicate the DEVPOD_HOME namespacing rules (they move with the
//     server, not the client), or
//   - combine the session-env and connect calls into one stanza.
//
// An older lakitu that doesn't know this method returns `method_not_found`;
// the client falls back to its pre-kart.connect shape
// (`[devpod, ssh, <name>]` plus a separate kart.session_env probe) so
// upgrades are smooth in either direction.
type KartConnectResult struct {
	Argv []string `json:"argv"`
	// ForwardPorts mirrors the `forwardPorts` array from the kart's
	// resolved devcontainer.json. The client unions these into its
	// ports.yaml with source=devcontainer so they survive across
	// sessions and don't depend on devpod's per-shell forward injection.
	// Empty / missing is a normal "no auto-forwards" case, not an error.
	ForwardPorts []int `json:"forward_ports,omitempty"`
}
