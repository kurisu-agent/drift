package wire

// ServerVersion is the shape returned by the server.version RPC.
// Shared between the server handler, the drift compat checker, and
// the CLI probe.
type ServerVersion struct {
	Version string `json:"version"`
	API     int    `json:"api"`
}
