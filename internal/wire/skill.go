package wire

// Skill is a Claude skill discovered in `~/.claude/skills/<name>/SKILL.md`
// on the circuit. Metadata only — the command the client runs to invoke
// the skill is rendered by [SkillResolveResult]. Parallel to [RunEntry]
// but narrower: skills don't declare arg shapes (a single prompt is the
// universal input) and they don't have modes (always interactive).
type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type SkillListResult struct {
	Skills []Skill `json:"skills"`
}

// SkillResolveParams: Name is the skill directory name; Prompt is the
// user's initial message. Both are forwarded as-is to the server-side
// template that builds the claude invocation.
type SkillResolveParams struct {
	Name   string `json:"name"`
	Prompt string `json:"prompt,omitempty"`
}

// SkillResolveResult mirrors [RunResolveResult] — Command is a shell
// snippet the client passes to ssh/mosh verbatim, Post names a client
// post-hook. Skills always run interactive, so no Mode field.
type SkillResolveResult struct {
	Name    string      `json:"name"`
	Post    RunPostHook `json:"post,omitempty"`
	Command string      `json:"command"`
}
