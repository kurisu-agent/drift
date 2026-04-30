// Package version surfaces build metadata. Values are injected by the
// release build via -ldflags; debug.ReadBuildInfo provides the fallback.
package version

import (
	"runtime/debug"
	"strings"
	"sync"
)

// Injected via -X internal/version.Version=... at release build time.
var (
	Version = ""
	Commit  = ""
	Date    = ""

	// APISchema: bumped only on breaking JSON-RPC surface changes.
	APISchema = 1
)

type Info struct {
	Version   string
	Commit    string
	Date      string
	APISchema int
}

var readInfo = sync.OnceValue(func() Info {
	info := Info{
		Version:   Version,
		Commit:    Commit,
		Date:      Date,
		APISchema: APISchema,
	}
	if info.Version != "" {
		return info
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		info.Version = "devel"
		return info
	}
	info.Version = bi.Main.Version
	if info.Version == "" || info.Version == "(devel)" {
		info.Version = "devel"
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			if info.Commit == "" {
				info.Commit = s.Value
			}
		case "vcs.time":
			if info.Date == "" {
				info.Date = s.Value
			}
		}
	}
	return info
})

func Get() Info { return readInfo() }

// Format renders `<binary> <Version> (<short-commit>)` for human text
// output. The short-commit suffix is appended only when Commit is set
// AND doesn't already appear in Version (nix-built binaries sometimes
// stuff the hash into Version via ldflags). Shared between drift and
// lakitu so a single version surface stays consistent.
func (i Info) Format(binary string) string {
	out := binary + " " + i.Version
	c := shortCommit(i.Commit)
	if c != "" && !strings.Contains(i.Version, c) {
		out += " (" + c + ")"
	}
	return out
}

func shortCommit(c string) string {
	const short = 7
	if len(c) > short {
		return c[:short]
	}
	return c
}
