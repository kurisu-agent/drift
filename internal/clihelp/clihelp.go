// Package clihelp renders an LLM-oriented help document for a Kong CLI:
// a terse one-line-per-leaf-command catalog plus caller-supplied extra
// sections. Flags and branch nodes are intentionally omitted — callers
// run `<tool> <cmd> --help` for that detail.
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

	if _, err := fmt.Fprintf(w, "COMMANDS (run `%s <cmd> --help` for flags)\n", app.Name); err != nil {
		return err
	}
	cmds := collectCommands(app.Node, nil)
	sort.SliceStable(cmds, func(i, j int) bool { return cmds[i].path < cmds[j].path })
	for _, c := range cmds {
		if _, err := fmt.Fprintf(w, "  %s — %s\n", c.path, c.help); err != nil {
			return err
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
	path string
	help string
}

// collectCommands returns leaf-only commands: branch nodes like `circuit`
// are skipped since their children carry the real semantics and the
// catalog stays tight.
func collectCommands(n *kong.Node, prefix []string) []renderedCmd {
	var out []renderedCmd
	for _, child := range n.Children {
		if child.Hidden || child.Type != kong.CommandNode {
			continue
		}
		path := append(append([]string(nil), prefix...), child.Name)
		if hasCommandChildren(child) {
			out = append(out, collectCommands(child, path)...)
			continue
		}
		out = append(out, renderedCmd{
			path: strings.Join(path, " "),
			help: child.Help,
		})
	}
	return out
}

func hasCommandChildren(n *kong.Node) bool {
	for _, c := range n.Children {
		if !c.Hidden && c.Type == kong.CommandNode {
			return true
		}
	}
	return false
}
