package drift

import (
	"fmt"
	"io"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/cli/style"
	"github.com/kurisu-agent/drift/internal/clihelp"
)

// helpCmd emits a curated catalog by default. --full falls back to the
// Kong-derived catalog (every leaf command + RPC methods + exit codes)
// for when an agent or human wants the kitchen sink.
type helpCmd struct {
	Full bool `help:"Print the full Kong-derived catalog, including RPC methods."`
}

// driftHelpSections is hand-curated, not derived from Kong, so it can group
// related kart/circuit verbs onto single lines and stay short. Keep it in
// sync when adding or renaming commands; --full is the auto-derived
// fallback that cannot drift.
var driftHelpSections = []struct {
	title string
	rows  [][2]string
}{
	{"CIRCUITS", [][2]string{
		{"circuit add|list|rm", "Manage circuits (SSH config + client state)"},
		{"circuit set default|name", "Set a circuit config field"},
		{"init", "Interactive first-time setup wizard"},
		{"status", "Show circuits + lakitu health + kart counts"},
		{"update", "Check GitHub for a newer drift and self-install"},
	}},
	{"KARTS", [][2]string{
		{"new <name>", "Create a kart (from starter or existing repo)"},
		{"list | info <name>", "List karts on the circuit or show one's state"},
		{"start|stop|restart|delete <name>", "Kart lifecycle (idempotent; delete is not)"},
		{"enable|disable <name>", "Toggle kart autostart on circuit reboot"},
		{"logs <name>", "Fetch a chunk of kart logs"},
		{"connect|into|attach <name>", "mosh (ssh fallback) into a kart; auto-starts"},
		{"ai", "Launch claude on the circuit (mosh/ssh)"},
	}},
}

const driftHelpFullIntro = `drift is the stateless workstation client. Every non-local call shells out
to ssh and invokes lakitu on a circuit via JSON-RPC 2.0. Server state lives
on the circuit under ~/.drift/garage/.`

// writeDriftHelp renders the curated catalog through the supplied palette so
// callers can emit ANSI on a TTY and plain text everywhere else (tests,
// pipes, NO_COLOR). Column widths are computed from the curated rows so the
// description column lines up across both sections.
func writeDriftHelp(w io.Writer, p *style.Palette) {
	cmdWidth := 0
	for _, sec := range driftHelpSections {
		for _, row := range sec.rows {
			if len(row[0]) > cmdWidth {
				cmdWidth = len(row[0])
			}
		}
	}
	cmdWidth += 2 // gutter before description

	fmt.Fprintf(w, "%s — %s\n", p.Bold(p.Accent("drift")), p.Bold("Devpod for drifters"))
	fmt.Fprintln(w, p.Dim("Remote devcontainers tuned for life on the move — persistent, agentic, phone-friendly"))

	for _, sec := range driftHelpSections {
		fmt.Fprintln(w)
		fmt.Fprintln(w, p.Bold(p.Accent(sec.title)))
		for _, row := range sec.rows {
			pad := strings.Repeat(" ", cmdWidth-len(row[0]))
			fmt.Fprintf(w, "  %s%s%s\n", p.Accent(row[0]), pad, row[1])
		}
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, p.Dim("Run `drift <cmd> --help` for flags · `drift help --full` for the full catalog"))
}

func runHelp(io IO, parser *kong.Kong, cmd helpCmd) int {
	if !cmd.Full {
		writeDriftHelp(io.Stdout, style.For(io.Stdout, false))
		return 0
	}
	doc := clihelp.Doc{
		App:   parser,
		Intro: driftHelpFullIntro,
		Sections: []clihelp.Section{
			clihelp.RPCMethodsSection(),
			clihelp.ExitCodesSection(),
		},
	}
	if err := clihelp.Render(io.Stdout, doc); err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	return 0
}
