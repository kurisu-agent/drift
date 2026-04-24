package drift

import (
	"strings"
)

// expandCLISSHArgs expands a leading `~/` / bare `~` against $HOME in
// every element of cliArgs so ssh receives concrete paths. Per-circuit
// options live in the `ssh:` map of the client config and are emitted
// as ssh_config directives (see internal/sshconf); only the one-off
// `drift connect mykart -- -i …` passthrough flows through here.
func expandCLISSHArgs(cliArgs []string) []string {
	if len(cliArgs) == 0 {
		return nil
	}
	home, err := userHome()
	if err != nil {
		return append([]string(nil), cliArgs...)
	}
	out := make([]string, len(cliArgs))
	for i, a := range cliArgs {
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
