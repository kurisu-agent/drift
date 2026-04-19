package drift

import (
	"fmt"

	"github.com/alecthomas/kong"
)

// helpCmd emits a curated catalog. Kong's --help covers per-command
// flags; this is the one point an agent can invoke to see the full
// command surface at a glance.
type helpCmd struct{}

// driftHelp is hand-curated, not derived from Kong, so it can group
// related kart/circuit verbs onto single lines and stay under ~20 lines.
// Keep it in sync when adding or renaming commands.
const driftHelp = `drift — stateless client for remote devcontainer workspaces.
Shells out via ssh and invokes lakitu on a circuit over JSON-RPC 2.0.
Run ` + "`drift <cmd> --help`" + ` for per-command flags. Output: -o text|json.

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

func runHelp(io IO, _ *kong.Kong) int {
	fmt.Fprint(io.Stdout, driftHelp)
	return 0
}
