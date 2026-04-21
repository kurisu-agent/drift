package drift

import (
	"context"
	"errors"

	"github.com/kurisu-agent/drift/internal/rpc/client"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/version"
)

// wrapCallWithCompat returns a deps.call that, on method_not_found,
// follows up with a cheap server.version probe so the surfaced error
// mentions the circuit's actual lakitu version and this drift's version
// — concrete info the user can act on ("update lakitu from 0.4.1 to
// ≥0.5.0") rather than the raw "method 'run.resolve' not implemented".
//
// The extra probe only fires on the failure path — successful RPCs pay
// no overhead. We deliberately do NOT pre-flight every call with a
// version probe: drift is one RPC per ssh per invocation, so a pre-flight
// would double the wire cost for every command. method_not_found is the
// only diagnostic that needs the version info.
func wrapCallWithCompat(base func(ctx context.Context, circuit, method string, params, out any) error, probe func(ctx context.Context, circuit string) (*probeResult, error)) func(ctx context.Context, circuit, method string, params, out any) error {
	return func(ctx context.Context, circuit, method string, params, out any) error {
		err := base(ctx, circuit, method, params, out)
		if err == nil {
			return nil
		}
		var re *rpcerr.Error
		if !errors.As(err, &re) || re.Type != "method_not_found" {
			return err
		}
		// Probe errors (network glitch, same method_not_found) fall back
		// to the plain hint so the user still sees something actionable.
		hint := versionMismatchError(re, method, "", 0)
		if probe != nil {
			if pr, pErr := probe(ctx, circuit); pErr == nil && pr != nil {
				hint = versionMismatchError(re, method, pr.Version, pr.API)
			}
		}
		return hint
	}
}

// versionMismatchError renders the actionable "update lakitu" hint. If
// serverVer is empty the probe failed — the fallback message omits
// concrete version numbers but still flags the root cause.
func versionMismatchError(orig *rpcerr.Error, method, serverVer string, serverAPI int) error {
	info := version.Get()
	if serverVer == "" {
		return rpcerr.New(orig.Code, orig.Type,
			"circuit's lakitu is older than this drift (missing %s); update lakitu on the circuit and retry. drift=%s api=%d",
			method, info.Version, info.APISchema,
		).With("method", method).With("client_version", info.Version).Wrap(orig)
	}
	return rpcerr.New(orig.Code, orig.Type,
		"circuit's lakitu is too old (version=%s api=%d, missing %s); this drift is %s api=%d. update lakitu on the circuit and retry",
		serverVer, serverAPI, method, info.Version, info.APISchema,
	).With("method", method).
		With("server_version", serverVer).
		With("server_api", serverAPI).
		With("client_version", info.Version).
		With("client_api", info.APISchema).
		Wrap(orig)
}

// defaultCall composes client.Call with the compat wrapper. Exported so
// defaultDeps() stays readable and tests can rebuild the wiring from
// component parts.
func defaultCall(c *client.Client) func(ctx context.Context, circuit, method string, params, out any) error {
	return wrapCallWithCompat(c.Call, defaultProbe(c))
}
