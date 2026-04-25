package wire

// KartProbePortsParams asks lakitu to enumerate listening TCP ports
// inside the kart. Name is the kart to probe; the kart must be running.
type KartProbePortsParams struct {
	Name string `json:"name"`
}

// KartProbePortsResult is the deduplicated, sorted list of TCP listeners
// inside the kart. Lakitu owns the parsing because the in-kart listing
// tool (`ss -tlnpH`) only exists server-side and the client should
// never need to know how to talk to a devcontainer directly. An empty
// slice is a normal result, not an error.
type KartProbePortsResult struct {
	Listeners []ProbeListener `json:"listeners"`
}

// ProbeListener is one (port, process-name) pair from `ss -tlnpH`.
// Process is best-effort: ss only attaches it when the calling user
// owns the listening process (or runs as root, which we don't), so
// listeners owned by another user inside the kart get an empty
// Process. The picker still surfaces the port either way.
type ProbeListener struct {
	Port    int    `json:"port"`
	Process string `json:"process,omitempty"`
}
