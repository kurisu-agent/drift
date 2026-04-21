package wire

// RunMode is the dispatch kind for a registry entry. The client reads this
// to decide transport (mosh for interactive, plain ssh for output) and how
// to render results.
type RunMode string

const (
	RunModeInteractive RunMode = "interactive"
	RunModeOutput      RunMode = "output"
)

// RunPostHook is a named post-exit hook the client knows how to handle.
// New hooks require a client release; new entries do not.
type RunPostHook string

const (
	RunPostNone                RunPostHook = ""
	RunPostConnectLastScaffold RunPostHook = "connect-last-scaffold"
)

// RunEntry is a registry entry as surfaced by run.list — metadata only, no
// command string (that's what run.resolve is for).
type RunEntry struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Mode        RunMode     `json:"mode"`
	Post        RunPostHook `json:"post,omitempty"`
}

type RunListResult struct {
	Entries []RunEntry `json:"entries"`
}

// RunResolveParams: Args is the positional tail from `drift run <name> …`.
// The server expands the command template with these values.
type RunResolveParams struct {
	Name string   `json:"name"`
	Args []string `json:"args,omitempty"`
}

// RunResolveResult: Command is a shell snippet the client passes to
// ssh/mosh verbatim. Expansion happens on the server so the client never
// needs to know the YAML schema or template funcs.
type RunResolveResult struct {
	Name    string      `json:"name"`
	Mode    RunMode     `json:"mode"`
	Post    RunPostHook `json:"post,omitempty"`
	Command string      `json:"command"`
}
