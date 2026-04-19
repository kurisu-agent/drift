package client

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"

	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/version"
	"github.com/kurisu-agent/drift/internal/wire"
)

type compatKey struct{ circuit string }

// CompatChecker runs the probe at most once per circuit. Safe for
// concurrent use.
type CompatChecker struct {
	client  *Client
	once    sync.Map // map[compatKey]*sync.Once
	results sync.Map // map[compatKey]*compatOutcome
}

type compatOutcome struct {
	warn string
	err  error
}

func NewCompatChecker(c *Client) *CompatChecker {
	return &CompatChecker{client: c}
}

// Check probes circuit's lakitu version and applies:
//   - major mismatch → typed rpcerr returned
//   - minor mismatch → single warning line to warnWriter
//   - patch mismatch → silent
//
// The version parser accepts `devel`, `v1.2.3`, and trailing `-pre` /
// `+meta` so drift dev builds (which report "devel") don't abort every
// call during local hacking.
func (c *CompatChecker) Check(ctx context.Context, circuit string, warnWriter io.Writer) error {
	key := compatKey{circuit: circuit}
	onceAny, _ := c.once.LoadOrStore(key, &sync.Once{})
	once, _ := onceAny.(*sync.Once)
	once.Do(func() {
		c.results.Store(key, c.runProbe(ctx, circuit))
	})
	outcomeAny, _ := c.results.Load(key)
	out, _ := outcomeAny.(*compatOutcome)
	if out.warn != "" && warnWriter != nil {
		fmt.Fprintln(warnWriter, out.warn)
	}
	return out.err
}

func (c *CompatChecker) runProbe(ctx context.Context, circuit string) *compatOutcome {
	var remote wire.ServerVersion
	if err := c.client.Call(ctx, circuit, wire.MethodServerVersion, nil, &remote); err != nil {
		return &compatOutcome{err: err}
	}
	local := version.Get().Version
	return compareSemver(local, remote.Version, circuit)
}

func compareSemver(local, remote, circuit string) *compatOutcome {
	lv, ok1 := parseSemver(local)
	rv, ok2 := parseSemver(remote)
	if !ok1 || !ok2 {
		// One side is "devel" or unparseable — stay silent. Local dev
		// builds routinely report "devel" and blocking them would make
		// hacking unpleasant.
		return &compatOutcome{}
	}
	switch {
	case lv.major != rv.major:
		return &compatOutcome{
			err: rpcerr.UserError(rpcerr.Type("version_mismatch"),
				"drift %s is incompatible with lakitu %s on circuit %q (major version mismatch)",
				local, remote, circuit).
				With("local", local).With("remote", remote).With("circuit", circuit),
		}
	case lv.minor != rv.minor:
		return &compatOutcome{
			warn: fmt.Sprintf("warning: drift %s and lakitu %s on circuit %q differ on minor version",
				local, remote, circuit),
		}
	default:
		return &compatOutcome{}
	}
}

type semver struct{ major, minor, patch int }

// parseSemver accepts `1.2.3`, `v1.2.3`, and trailing `-pre` / `+meta`.
// Returns ok=false for anything else (including "devel").
func parseSemver(s string) (semver, bool) {
	s = strings.TrimPrefix(s, "v")
	// Drop build metadata and pre-release tags — not needed for
	// major/minor/patch equality comparison.
	for _, sep := range []string{"+", "-"} {
		if i := strings.Index(s, sep); i >= 0 {
			s = s[:i]
		}
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	var out semver
	ns := []*int{&out.major, &out.minor, &out.patch}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return semver{}, false
		}
		*ns[i] = n
	}
	return out, true
}
