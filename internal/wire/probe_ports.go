package wire

// KartProbePortsParams asks lakitu to enumerate ports the kart wants
// forwarded. Name is the kart to probe. The kart does not have to be
// running: when it isn't, Listeners is empty but DevcontainerPorts is
// still populated from the kart's devcontainer.json.
type KartProbePortsParams struct {
	Name string `json:"name"`
}

// KartProbePortsResult bundles two ways of asking "what should we
// forward?" — the live `ss -tlnpH` listeners *and* the static
// forwardPorts from devcontainer.json. The client unions both when
// presenting the picker so users can pre-select the kart's declared
// ports before any process inside has bound them. Both fields may be
// empty (no listeners + no spec); that's a normal "nothing to forward"
// result, not an error.
type KartProbePortsResult struct {
	Listeners         []ProbeListener `json:"listeners"`
	DevcontainerPorts []int           `json:"devcontainer_ports,omitempty"`
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
