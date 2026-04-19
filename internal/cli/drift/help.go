package drift

import (
	"github.com/alecthomas/kong"
	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/clihelp"
)

// helpCmd emits the LLM-oriented catalog. Kong's --help covers humans;
// this is the one point an agent can invoke to get everything at once.
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
		return errfmt.Emit(io.Stderr, err)
	}
	return 0
}
