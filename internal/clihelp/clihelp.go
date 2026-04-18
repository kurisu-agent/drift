// Package clihelp renders an LLM-oriented help document for a Kong CLI.
//
// The shape is intentionally flat and predictable: name + one-liner, a
// flat command catalog (parent-space-child), per-command flags, and any
// caller-supplied extra sections (RPC methods, state layout, exit codes).
// A human is a secondary audience — Kong's own --help covers that — so
// this renderer leans toward dense, grep-friendly text rather than pretty
// tables.
package clihelp

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/alecthomas/kong"
)

// Doc is the input to [Render]. App.Model is the root of the Kong parse
// tree; Sections are emitted after the command catalog in the order given.
type Doc struct {
	// App is the parsed Kong model. Render walks App.Model recursively.
	App *kong.Kong
	// Intro is a free-form paragraph printed after the NAME line. Empty
	// intros are skipped.
	Intro string
	// Sections are extra structured blocks (RPC methods, state layout,
	// exit codes, …) appended after the command list. Empty-body sections
	// are skipped so callers can conditionally include them.
	Sections []Section
}

// Section is one titled block of the rendered help. Body is emitted
// verbatim, so callers pre-format bullet points, indentation, etc.
type Section struct {
	Title string
	Body  string
}

// Render writes the LLM help for d to w. It never returns an error from
// the renderer itself; only Write failures propagate.
func Render(w io.Writer, d Doc) error {
	if d.App == nil || d.App.Model == nil {
		return fmt.Errorf("clihelp: nil Kong model")
	}
	app := d.App.Model

	// Kong's Description often embeds the name ("foo — a thing"); strip
	// the redundant prefix so the NAME line doesn't double up.
	tagline := strings.TrimSpace(strings.TrimPrefix(app.Help, app.Name))
	tagline = strings.TrimPrefix(tagline, "—")
	tagline = strings.TrimPrefix(tagline, "-")
	tagline = strings.TrimSpace(tagline)
	if tagline == "" {
		tagline = app.Help
	}
	if _, err := fmt.Fprintf(w, "NAME\n  %s — %s\n\n", app.Name, tagline); err != nil {
		return err
	}
	if strings.TrimSpace(d.Intro) != "" {
		if _, err := fmt.Fprintf(w, "%s\n\n", strings.TrimRight(d.Intro, "\n")); err != nil {
			return err
		}
	}

	// Global flags — the root Node's own flags, minus Kong's auto-added
	// --help (which is never useful to an LLM).
	if flags := filterFlags(app.Flags); len(flags) > 0 {
		if _, err := fmt.Fprintln(w, "GLOBAL FLAGS"); err != nil {
			return err
		}
		writeFlags(w, flags)
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
	}

	if _, err := fmt.Fprintln(w, "COMMANDS"); err != nil {
		return err
	}
	cmds := collectCommands(app.Node, nil)
	sort.SliceStable(cmds, func(i, j int) bool { return cmds[i].path < cmds[j].path })
	for _, c := range cmds {
		if _, err := fmt.Fprintf(w, "  %s — %s\n", c.path, c.help); err != nil {
			return err
		}
		if len(c.flags) > 0 {
			writeFlags(w, c.flags)
		}
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}

	for _, s := range d.Sections {
		if strings.TrimSpace(s.Body) == "" {
			continue
		}
		if _, err := fmt.Fprintf(w, "%s\n%s\n\n", s.Title, strings.TrimRight(s.Body, "\n")); err != nil {
			return err
		}
	}
	return nil
}

type renderedCmd struct {
	path  string
	help  string
	flags []*kong.Flag
}

// collectCommands walks the Kong tree depth-first and returns one entry
// per leaf/branch command. Hidden commands are skipped — they're always
// internal scaffolding (e.g. drift's ssh-proxy).
func collectCommands(n *kong.Node, prefix []string) []renderedCmd {
	var out []renderedCmd
	for _, child := range n.Children {
		if child.Hidden || child.Type != kong.CommandNode {
			continue
		}
		path := append(append([]string(nil), prefix...), child.Name)
		// Include intermediate nodes too — a branch like `circuit` is
		// itself meaningful ("drift circuit" groups add/rm/list) even
		// though its children carry the real semantics.
		out = append(out, renderedCmd{
			path:  strings.Join(path, " "),
			help:  child.Help,
			flags: filterFlags(child.Flags),
		})
		out = append(out, collectCommands(child, path)...)
	}
	return out
}

// filterFlags drops Kong's auto-generated --help. That flag exists on
// every node and carries no information an LLM doesn't already know.
func filterFlags(in []*kong.Flag) []*kong.Flag {
	out := in[:0:0]
	for _, f := range in {
		if f == nil || f.Hidden || f.Name == "help" {
			continue
		}
		out = append(out, f)
	}
	return out
}

func writeFlags(w io.Writer, flags []*kong.Flag) {
	for _, f := range flags {
		name := "--" + f.Name
		if f.Short != 0 {
			name = fmt.Sprintf("-%c, %s", f.Short, name)
		}
		help := f.Help
		if help == "" {
			help = "(no description)"
		}
		_, _ = fmt.Fprintf(w, "      %s  %s\n", name, help)
	}
}
