package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/systemd"
	"github.com/kurisu-agent/drift/internal/wire"
)

// KartAutostartDeps bundles the collaborators the kart.enable / kart.disable
// handlers need. It is a separate dep struct from [KartDeps] so Phase 12 can
// land without touching Phase 7/9 wiring.
type KartAutostartDeps struct {
	// GarageDir is the absolute path to ~/.drift/garage. The handlers
	// maintain an `autostart` marker file under karts/<name>/ in sync with
	// systemd state.
	GarageDir string
	// Systemd is the thin systemctl --user wrapper. Required.
	Systemd *systemd.Client
}

// RegisterKartAutostart wires the Phase 12 handlers into reg.
func RegisterKartAutostart(reg *rpc.Registry, d KartAutostartDeps) {
	reg.Register(wire.MethodKartEnable, d.kartEnableHandler)
	reg.Register(wire.MethodKartDisable, d.kartDisableHandler)
}

// AutostartResult is the envelope returned by kart.enable / kart.disable.
// `enabled` reflects the final state — after a successful enable it's true,
// after disable it's false. Both fields are populated on every success so
// drift can render a one-line status without a second RPC.
type AutostartResult struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

func (d *KartAutostartDeps) kartEnableHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p KartLifecycleParams
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	if err := requireKartName(p.Name); err != nil {
		return nil, err
	}
	if d.Systemd == nil {
		return nil, rpcerr.Internal("systemd client not configured")
	}
	if err := d.Systemd.Enable(ctx, p.Name); err != nil {
		return nil, wrapSystemdError(err)
	}
	if err := d.writeAutostartMarker(p.Name); err != nil {
		return nil, rpcerr.Internal("write autostart marker: %v", err).Wrap(err)
	}
	return AutostartResult{Name: p.Name, Enabled: true}, nil
}

func (d *KartAutostartDeps) kartDisableHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p KartLifecycleParams
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	if err := requireKartName(p.Name); err != nil {
		return nil, err
	}
	if d.Systemd == nil {
		return nil, rpcerr.Internal("systemd client not configured")
	}
	if err := d.Systemd.Disable(ctx, p.Name); err != nil {
		return nil, wrapSystemdError(err)
	}
	if err := d.removeAutostartMarker(p.Name); err != nil {
		return nil, rpcerr.Internal("remove autostart marker: %v", err).Wrap(err)
	}
	return AutostartResult{Name: p.Name, Enabled: false}, nil
}

// autostartMarkerPath returns the path of the presence-marker file that
// signals "this kart has autostart on" to lakitu init's reconciliation pass.
func (d *KartAutostartDeps) autostartMarkerPath(kart string) string {
	return filepath.Join(d.GarageDir, "karts", kart, "autostart")
}

func (d *KartAutostartDeps) writeAutostartMarker(kart string) error {
	path := d.autostartMarkerPath(kart)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// Empty file; the presence of the path is the signal.
	return os.WriteFile(path, nil, 0o600)
}

func (d *KartAutostartDeps) removeAutostartMarker(kart string) error {
	err := os.Remove(d.autostartMarkerPath(kart))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// wrapSystemdError maps systemd-specific errors to the rpcerr catalog: a
// *systemd.DenialError becomes code:6 systemd_denied with a suggestion;
// anything else is surfaced as code:5 (the systemd integration belongs to
// the devpod-adjacent "external process failure" bucket since plans/PLAN.md
// doesn't split it out).
func wrapSystemdError(err error) error {
	if err == nil {
		return nil
	}
	var de *systemd.DenialError
	if errors.As(err, &de) {
		return rpcerr.New(rpcerr.CodeAuth, rpcerr.TypeSystemdDenied, "%s", de.Error()).
			With("suggestion", "ensure `loginctl enable-linger <user>` has been run on the circuit")
	}
	return rpcerr.New(rpcerr.CodeDevpod, "systemctl_failed", "%v", err).Wrap(err)
}

// requireKartName mirrors the shared validator used elsewhere in this
// package. Kept as a thin wrapper so we don't drift from the existing
// check in kart_lifecycle.go.
func requireKartName(n string) error {
	if n == "" {
		return rpcerr.UserError(rpcerr.TypeInvalidFlag, "kart name is required")
	}
	return nil
}
