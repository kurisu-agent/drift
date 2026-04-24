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

// driftHelpRows is a hand-curated flat top-N, ordered along the new-user
// path (init → status → connect) before the rest. Keep in sync when adding
// or renaming commands; --full is the auto-derived fallback that cannot
// drift and surfaces every leaf command, including ones omitted here
// (circuit *, kart subverbs, migrate, …).
var driftHelpRows = [][2]string{
	{"init", "Interactive first-time setup wizard (circuits + characters)"},
	{"status", "Circuits + lakitu health + per-circuit karts"},
	{"connect [<name>]", "Mosh/ssh into a circuit or kart (merged picker)"},
	{"new <name>", "Create a kart (from starter or existing repo)"},
	{"karts", "List karts across circuits (cross-circuit; -c scopes)"},
	{"start | stop | delete [<name>]", "Lifecycle shortcuts; bare drops into the cross-circuit kart picker"},
	{"kart <verb> <name>", "Full lifecycle: start / stop / restart / recreate / rebuild / delete / logs / info / enable / disable"},
	{"ai", "Launch Claude Code on the circuit"},
	{"skill [<name> [prompt]]", "Pick / invoke a Claude skill (`drift skills` to list)"},
	{"run [<name>]", "Execute a user-script shorthand (`drift runs` to list)"},
	{"circuits", "List configured circuits"},
	{"circuit <verb>", "Manage circuits: add / rm / set (name|default) / connect"},
	{"migrate", "Adopt an existing devpod workspace as a drift kart"},
	{"update", "Check GitHub for a newer drift and self-install"},
}

const driftHelpFullIntro = `drift is the stateless workstation client. Every non-local call shells out
to ssh and invokes lakitu on a circuit via JSON-RPC 2.0. Server state lives
on the circuit under ~/.drift/garage/.`

// writeDriftHelp renders the curated catalog through the supplied palette so
// callers can emit ANSI on a TTY and plain text everywhere else (tests,
// pipes, NO_COLOR). Column widths are computed from the curated rows so the
// description column lines up.
func writeDriftHelp(w io.Writer, p *style.Palette) {
	cmdWidth := 0
	for _, row := range driftHelpRows {
		if len(row[0]) > cmdWidth {
			cmdWidth = len(row[0])
		}
	}
	cmdWidth += 2 // gutter before description

	fmt.Fprintf(w, "%s — %s\n", p.Bold(p.Accent("drift")), p.Bold("Devpod for drifters"))
	fmt.Fprintln(w, p.Dim("Remote devcontainers tuned for life on the move — persistent, agentic, phone-friendly"))

	fmt.Fprintln(w)
	fmt.Fprintf(w, "%s  %s  %s\n",
		p.Bold(p.Accent("▶")),
		p.Bold("Full catalog:"),
		p.Bold(p.Accent("drift help --full")),
	)

	fmt.Fprintln(w)
	fmt.Fprintln(w, p.Bold(p.Accent("COMMANDS")))
	for _, row := range driftHelpRows {
		pad := strings.Repeat(" ", cmdWidth-len(row[0]))
		fmt.Fprintf(w, "  %s%s%s\n", p.Accent(row[0]), pad, row[1])
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, p.Dim("Run `drift <cmd> --help` for per-command flags"))
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
