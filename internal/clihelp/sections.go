package clihelp

import (
	"sort"
	"strings"

	"github.com/kurisu-agent/drift/internal/config"
	"github.com/kurisu-agent/drift/internal/wire"
)

func ExitCodesSection() Section {
	return Section{
		Title: "EXIT CODES",
		Body:  "  0 success · 2 user error · 3 not-found · 4 conflict",
	}
}

// RPCMethodsSection derives from [wire.Methods] so adding a method shows
// up here automatically.
func RPCMethodsSection() Section {
	methods := append([]string(nil), wire.Methods()...)
	sort.Strings(methods)
	var b strings.Builder
	for _, m := range methods {
		b.WriteString("  ")
		b.WriteString(m)
		b.WriteString("\n")
	}
	return Section{Title: "RPC METHODS", Body: b.String()}
}

// GarageLayoutSection derives from config.GarageSubdirs so a new subdir
// appears automatically.
func GarageLayoutSection() Section {
	var b strings.Builder
	b.WriteString("  ~/.drift/\n")
	b.WriteString("    CLAUDE.md            # this file\n")
	b.WriteString("    garage/\n")
	b.WriteString("      config.yaml        # server config (server schema)\n")
	for _, sub := range config.GarageSubdirs {
		suffix := ""
		if sub == "chest" {
			suffix = "  # mode 0700, holds secrets"
		}
		b.WriteString("      ")
		b.WriteString(sub)
		b.WriteString("/")
		b.WriteString(suffix)
		b.WriteString("\n")
	}
	return Section{Title: "STATE LAYOUT (this machine)", Body: b.String()}
}
