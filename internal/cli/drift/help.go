package drift

import (
	"github.com/alecthomas/kong"
	"github.com/kurisu-agent/drift/internal/clihelp"
)

// helpCmd is `drift help`. It emits a dense, LLM-oriented catalog of drift's
// subcommands and the shared drift↔lakitu conventions. Kong's `--help` flag
// is still present on every node for humans; this subcommand is the one
// point an agent running at the terminal can invoke to get everything in
// one place.
type helpCmd struct{}

const driftHelpIntro = `drift is the stateless workstation client. Every non-local call shells out
to ssh and invokes lakitu on a circuit via JSON-RPC 2.0. Server state lives
on the circuit under ~/.drift/garage/.`

func runHelp(io IO, parser *kong.Kong) int {
	doc := clihelp.Doc{
		App:   parser,
		Intro: driftHelpIntro,
		Sections: []clihelp.Section{
			clihelp.RPCMethodsSection(),
			clihelp.ExitCodesSection(),
		},
	}
	if err := clihelp.Render(io.Stdout, doc); err != nil {
		return emitError(io, err)
	}
	return 0
}
