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

// ServerVersion is the shape returned by the server.version RPC. Duplicated
// here rather than imported from internal/server so the drift client stays
// independent of the server package's file layout.
type ServerVersion struct {
	Version string `json:"version"`
	API     int    `json:"api"`
}

// CheckCompat fetches lakitu's version over the RPC client and compares it
// to the drift binary's own semver. The result is cached per (client,
// circuit) pair — subsequent calls in the same process return without
// issuing another RPC. `--skip-version-check` bypasses the check entirely
// and callers can signal that by not invoking CheckCompat at all, or by
// passing a zero skip=true on the return value.
//
// Semver comparison mirrors plans/PLAN.md § Version compatibility:
//
//   - major mismatch  → abort with a typed rpcerr
//   - minor mismatch  → write a single warning line to stderr
//   - patch mismatch  → silent
//
// The version parser is deliberately lax — it accepts `devel`,
// `v1.2.3`, `1.2.3`, trailing `-prerelease` or `+metadata` — because drift
// development builds report "devel" and we don't want that to abort every
// RPC call during local hacking.
type compatKey struct{ circuit string }

// CompatChecker wraps a Client and runs the compat probe at most once per
// circuit. It's safe for concurrent use.
type CompatChecker struct {
	client  *Client
	once    sync.Map // map[compatKey]*sync.Once
	results sync.Map // map[compatKey]*compatOutcome
}

type compatOutcome struct {
	warn string // set when a minor mismatch should be logged
	err  error  // set when the call must abort
}

// NewCompatChecker returns a checker backed by c. The zero value is not
// usable.
func NewCompatChecker(c *Client) *CompatChecker {
	return &CompatChecker{client: c}
}

// Check runs the probe for circuit. Transport failures surface as-is (a
// *TransportError); ordinary RPC errors from server.version are wrapped as
// an internal rpcerr so the caller can distinguish probe failure from the
// eventual request failure.
//
// When the server is on an older minor, Check writes a single line to
// warnWriter. The returned error is non-nil only on major mismatch or on
// probe failure.
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
	var remote ServerVersion
	if err := c.client.Call(ctx, circuit, wire.MethodServerVersion, nil, &remote); err != nil {
		return &compatOutcome{err: err}
	}
	local := version.Get().Version
	return compareSemver(local, remote.Version, circuit)
}

// compareSemver applies the major/minor/patch rules. Exposed for tests.
func compareSemver(local, remote, circuit string) *compatOutcome {
	lv, ok1 := parseSemver(local)
	rv, ok2 := parseSemver(remote)
	if !ok1 || !ok2 {
		// One side is "devel" or unparseable — don't block the call, just
		// stay silent. Local dev builds routinely report "devel"; pinning
		// them against a tagged lakitu would make hacking unpleasant.
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

// parseSemver accepts `1.2.3`, `v1.2.3`, trailing `-pre` / `+meta` suffix,
// and returns (_, false) for anything else (including "devel"). Strict
// enough to catch typos, lax enough to survive pre-release builds.
func parseSemver(s string) (semver, bool) {
	s = strings.TrimPrefix(s, "v")
	// Strip build metadata (`+...`) and pre-release tag (`-...`) — neither
	// is semver-sorted-comparable without more work than we need for
	// major/minor/patch equality.
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
