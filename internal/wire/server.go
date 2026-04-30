package wire

// ServerVersion is the shape returned by the server.version RPC.
// Shared between the server handler, the drift compat checker, and
// the CLI probe.
type ServerVersion struct {
	Version string `json:"version"`
	API     int    `json:"api"`
}

// ServerInfo is the richer one-shot response returned by server.info. It
// superset of ServerVersion plus the circuit identity (Name) and any
// defaults the client needs for idempotent setup (DefaultCharacter).
// server.version stays on the hot path for version compatibility checks;
// server.info is reserved for setup-time flows like `drift circuit add`
// and `drift init`.
type ServerInfo struct {
	Version string `json:"version"`
	API     int    `json:"api"`
	Name    string `json:"name"`
	// Icon is the source-of-truth icon string — either a nerd-font name
	// ("dev-go") or an emoji literal ("🚀"). Empty when unset. Clients
	// render via internal/icons.Resolve.
	Icon             string `json:"icon,omitempty"`
	DefaultCharacter string `json:"default_character,omitempty"`
}
