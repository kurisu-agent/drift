package wire

// RunMode and RunPostHook live in dispatch.go — shared across
// `drift ai`, `drift skill`, and the `drift run` shorthand dispatcher.

// RunArgType is the client-side input widget the registry asks for when a
// user invokes a run with no positional args. Empty means RunArgTypeInput
// (a single-line input) — the most common case, so it's the default.
type RunArgType string

const (
	RunArgTypeInput  RunArgType = "input"
	RunArgTypeText   RunArgType = "text"
	RunArgTypeSelect RunArgType = "select"
)

// RunArgSpec declares a single positional slot the client should prompt for
// when the user invokes `drift run <name>` interactively with no positional
// args. Prompt (when set) overrides Name as the widget label. Options is
// only meaningful for RunArgTypeSelect; Default pre-fills the widget and
// is used verbatim if the user leaves the field blank.
type RunArgSpec struct {
	Name    string     `json:"name" yaml:"name"`
	Prompt  string     `json:"prompt,omitempty" yaml:"prompt,omitempty"`
	Type    RunArgType `json:"type,omitempty" yaml:"type,omitempty"`
	Options []string   `json:"options,omitempty" yaml:"options,omitempty"`
	Default string     `json:"default,omitempty" yaml:"default,omitempty"`
}

// RunEntry is a registry entry as surfaced by run.list — metadata only, no
// command string (that's what run.resolve is for).
type RunEntry struct {
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"`
	Mode        RunMode      `json:"mode"`
	Post        RunPostHook  `json:"post,omitempty"`
	Args        []RunArgSpec `json:"args,omitempty"`
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
