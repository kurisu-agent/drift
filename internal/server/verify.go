package server

import (
	"context"
	"encoding/json"

	"github.com/kurisu-agent/drift/internal/devpod"
	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/wire"
)

// VerifyResult is the "heavy" setup-time probe — includes a devpod
// subprocess call, so hit it only from drift init / lakitu init rather
// than every RPC round-trip.
type VerifyResult struct {
	// Lakitu duplicates server.version's payload so verify is a drop-in
	// superset in one round-trip.
	Lakitu wire.ServerVersion `json:"lakitu"`
	// DevpodActual: empty means the probe failed; see DevpodError.
	DevpodActual string `json:"devpod_actual,omitempty"`
	// DevpodExpected: "" means dev build, no pin.
	DevpodExpected string `json:"devpod_expected,omitempty"`
	// DevpodMatch: clients should render mismatch as warning not error —
	// forks stay argv-compatible across minor bumps.
	DevpodMatch bool `json:"devpod_match"`
	// DevpodError: free-form `devpod version` stderr. Render, don't parse.
	DevpodError string `json:"devpod_error,omitempty"`
}

// VerifyHandler folds every problem into VerifyResult — never RPC-errors —
// so clients get a complete picture from one round-trip. Hangs off *Deps
// so lakitu's wired devpod client (the pinned one) is re-used — the
// previous package-level form always built a default-configured client and
// bypassed the pin.
func (d *Deps) VerifyHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p struct{}
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	dev := d.Devpod
	if dev == nil {
		dev = &devpod.Client{}
	}
	return verifyNow(ctx, dev), nil
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
