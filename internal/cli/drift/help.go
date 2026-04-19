package drift

import (
	"fmt"

	"github.com/alecthomas/kong"
	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/clihelp"
)

// helpCmd emits a curated catalog by default. --full falls back to the
// Kong-derived catalog (every leaf command + RPC methods + exit codes)
// for when an agent or human wants the kitchen sink.
type helpCmd struct {
	Full bool `help:"Print the full Kong-derived catalog, including RPC methods."`
}

// driftHelp is hand-curated, not derived from Kong, so it can group
// related kart/circuit verbs onto single lines and stay under ~20 lines.
// Keep it in sync when adding or renaming commands; --full is the
// auto-derived fallback that cannot drift.
const driftHelp = `drift — stateless client for remote devcontainer workspaces.
Shells out via ssh and invokes lakitu on a circuit over JSON-RPC 2.0.
Run ` + "`drift <cmd> --help`" + ` for per-command flags, ` + "`drift help --full`" + ` for all.

circuit add|list|rm               Manage circuits (SSH config + client state).
circuit set default|name          Set a circuit config field.
init                              Interactive first-time setup wizard.
status                            Show circuits + lakitu health + kart counts.
update                            Check GitHub for a newer drift and self-install.

new <name>                        Create a kart (from starter or existing repo).
list | info <name>                List karts on the circuit or show one's state.
start|stop|restart|delete <name>  Kart lifecycle (idempotent; delete is not).
enable|disable <name>             Toggle kart autostart on circuit reboot.
logs <name>                       Fetch a chunk of kart logs.
connect <name>                    mosh (ssh fallback) into a kart; auto-starts.
ai                                Launch claude on the circuit (mosh/ssh).

Exit: 0 ok · 2 user error · 3 not-found · 4 conflict.
`

const driftHelpFullIntro = `drift is the stateless workstation client. Every non-local call shells out
to ssh and invokes lakitu on a circuit via JSON-RPC 2.0. Server state lives
on the circuit under ~/.drift/garage/.`

func runHelp(io IO, parser *kong.Kong, cmd helpCmd) int {
	if !cmd.Full {
		fmt.Fprint(io.Stdout, driftHelp)
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
