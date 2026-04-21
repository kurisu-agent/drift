package lakitu

import (
	"github.com/alecthomas/kong"
	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/clihelp"
)

// helpCmd is the LLM-context one-stop for an agent on the circuit
// (typically launched via `drift run ai`).
type helpCmd struct{}

const lakituHelpIntro = `lakitu is the server half of drift. It is invoked two ways: as a long-lived
(per-request, stateless) RPC handler over ssh, driven by the drift client;
and as a human CLI on the circuit for state that has no wire surface
(init, raw chest/character inspection). State lives under ~/.drift/garage/.`

func runHelp(io IO, parser *kong.Kong) int {
	doc := clihelp.Doc{
		App:   parser,
		Intro: lakituHelpIntro,
		Sections: []clihelp.Section{
			clihelp.GarageLayoutSection(),
			clihelp.RPCMethodsSection(),
			clihelp.ExitCodesSection(),
		},
	}
	if err := clihelp.Render(io.Stdout, doc); err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	return 0
}
