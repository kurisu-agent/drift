package server

import (
	"context"
	"encoding/json"

	"github.com/kurisu-agent/drift/internal/devpod"
	"github.com/kurisu-agent/drift/internal/rpc"
)

// VerifyResult is the shape returned by `server.verify`. It's the
// dedicated "heavy" version probe — includes a devpod sanity check that
// shells out, so callers should hit it only during explicit setup steps
// (drift warmup, lakitu init) rather than on every RPC round-trip.
type VerifyResult struct {
	// Lakitu is the same payload `server.version` returns — duplicated
	// here so `server.verify` is a drop-in superset when the caller wants
	// both in one round-trip.
	Lakitu VersionResult `json:"lakitu"`
	// DevpodActual is the version the circuit's devpod binary reports.
	// Empty when the probe failed; DevpodError carries the detail then.
	DevpodActual string `json:"devpod_actual,omitempty"`
	// DevpodExpected is the version lakitu was built against — injected
	// via ldflag on internal/devpod.ExpectedVersion. Empty means "dev
	// build; no pin."
	DevpodExpected string `json:"devpod_expected,omitempty"`
	// DevpodMatch reflects DevpodActual == DevpodExpected (or
	// DevpodExpected is empty). Clients should surface a mismatch as a
	// warning, not a hard error — forks keep argv compatible across minor
	// bumps.
	DevpodMatch bool `json:"devpod_match"`
	// DevpodError, when non-empty, is the error string from `devpod
	// version`. Shape: a free-form message — do not parse, just render.
	DevpodError string `json:"devpod_error,omitempty"`
}

// VerifyHandler runs the setup-time sanity checks that would otherwise
// bloat every RPC round-trip: lakitu's own version, plus a live probe of
// the devpod binary. Never errors at the RPC level — problems are folded
// into the result so clients get a complete picture from one round-trip.
func VerifyHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p struct{}
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	return verifyNow(ctx, &devpod.Client{}), nil
}

func verifyNow(ctx context.Context, dev *devpod.Client) VerifyResult {
	res := VerifyResult{
		Lakitu:         Version(),
		DevpodExpected: devpod.ExpectedVersion,
	}
	vc, err := dev.Verify(ctx)
	if err != nil {
		res.DevpodError = err.Error()
		return res
	}
	res.DevpodActual = vc.Actual
	res.DevpodMatch = vc.Match
	return res
}

