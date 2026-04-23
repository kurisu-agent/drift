package drift

import (
	"strings"

	"github.com/kurisu-agent/drift/internal/config"
)

// mergeSSHArgs concatenates per-circuit config args (first) with CLI
// passthrough args (last), then expands a leading `~/` / bare `~` against
// $HOME so ssh receives a concrete path.
//
// Config-first / CLI-last matches how ssh resolves duplicate flags:
// last-wins options like `-p` or `-o Port=…` favor the CLI override, while
// accumulating options like `-i` pick up all keys the user supplied.
//
// cfg may be nil (test paths) and name may refer to a circuit that isn't
// in the config — both collapse to "just use cliArgs" rather than
// erroring, since the CLI passthrough stands on its own.
func mergeSSHArgs(cfg *config.Client, name string, cliArgs []string) []string {
	var circuitArgs []string
	if cfg != nil {
		if c, ok := cfg.Circuits[name]; ok {
			circuitArgs = c.SSHArgs
		}
	}
	if len(circuitArgs) == 0 && len(cliArgs) == 0 {
		return nil
	}
	out := make([]string, 0, len(circuitArgs)+len(cliArgs))
	out = append(out, circuitArgs...)
	out = append(out, cliArgs...)
	home, err := userHome()
	if err != nil {
		return out
	}
	for i, a := range out {
		out[i] = expandSSHTilde(a, home)
	}
	return out
}

// expandSSHTilde handles the two forms we see in configs: bare `~` and
// `~/foo`. `~user/` is left alone — ssh or the kernel may well resolve
// it, and reimplementing nsswitch here isn't worth it.
func expandSSHTilde(s, home string) string {
	if home == "" {
		return s
	}
	if s == "~" {
		return home
	}
	if strings.HasPrefix(s, "~/") {
		return home + s[1:]
	}
	return s
}
