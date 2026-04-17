// Package version surfaces build metadata to the CLI. Values are injected at
// release time via -ldflags; outside of release builds, debug.ReadBuildInfo
// provides a best-effort fallback.
package version

import (
	"runtime/debug"
	"sync"
)

var (
	// Version is the semver build tag (e.g. "1.4.2"). Release builds set it
	// via -X github.com/kurisu-agent/drift/internal/version.Version=...
	Version = ""
	// Commit is the VCS revision the binary was built from.
	Commit = ""
	// Date is the commit timestamp in RFC 3339 form, used for reproducible
	// builds (GoReleaser's mod_timestamp: {{.CommitTimestamp}}).
	Date = ""

	// APISchema is the integer JSON-RPC surface version — bumped only on
	// breaking wire changes. See PLAN.md § Version compatibility.
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

// Get returns the resolved build info, memoized on first call.
func Get() Info { return readInfo() }
