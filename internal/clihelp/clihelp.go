// Package clihelp renders an LLM-oriented help document for a Kong CLI:
// flat command catalog, per-command flags, and caller-supplied extra
// sections. Dense and grep-friendly — humans use Kong's own --help.
package clihelp

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/alecthomas/kong"
)

type Doc struct {
	App      *kong.Kong
	Intro    string
	Sections []Section
}

// Section.Body is emitted verbatim — callers pre-format bullets/indent.
type Section struct {
	Title string
	Body  string
}

func Render(w io.Writer, d Doc) error {
	if d.App == nil || d.App.Model == nil {
		return fmt.Errorf("clihelp: nil Kong model")
	}
	app := d.App.Model

	// Kong's Description often embeds the name ("foo — a thing"); strip
	// the redundant prefix so NAME doesn't double up.
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

func collectCommands(n *kong.Node, prefix []string) []renderedCmd {
	var out []renderedCmd
	for _, child := range n.Children {
		if child.Hidden || child.Type != kong.CommandNode {
			continue
		}
		path := append(append([]string(nil), prefix...), child.Name)
		// Include intermediate nodes too — a branch like `circuit` is
		// itself meaningful even though children carry the real semantics.
		out = append(out, renderedCmd{
			path:  strings.Join(path, " "),
			help:  child.Help,
			flags: filterFlags(child.Flags),
		})
		out = append(out, collectCommands(child, path)...)
	}
	return out
}

// filterFlags drops Kong's auto-generated --help, which carries no info
// an LLM doesn't already know.
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
