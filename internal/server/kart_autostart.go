package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/kurisu-agent/drift/internal/config"
	"github.com/kurisu-agent/drift/internal/name"
	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/systemd"
	"github.com/kurisu-agent/drift/internal/wire"
)

type KartAutostartDeps struct {
	GarageDir string
	Systemd   *systemd.Client
}

func RegisterKartAutostart(reg *rpc.Registry, d KartAutostartDeps) {
	reg.Register(wire.MethodKartEnable, d.kartEnableHandler)
	reg.Register(wire.MethodKartDisable, d.kartDisableHandler)
}

// AutostartResult.Enabled reflects the final state so drift can render a
// one-line status without a second RPC.
type AutostartResult struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

func (d *KartAutostartDeps) kartEnableHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p KartLifecycleParams
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	if err := name.Validate("kart", p.Name); err != nil {
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
	if err := name.Validate("kart", p.Name); err != nil {
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

// autostartMarkerPath: presence of this file signals "autostart on" to
// lakitu init's reconciliation pass.
func (d *KartAutostartDeps) autostartMarkerPath(kart string) string {
	return config.KartAutostartPath(d.GarageDir, kart)
}

func (d *KartAutostartDeps) writeAutostartMarker(kart string) error {
	path := d.autostartMarkerPath(kart)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, nil, 0o600)
}

func (d *KartAutostartDeps) removeAutostartMarker(kart string) error {
	err := os.Remove(d.autostartMarkerPath(kart))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// wrapSystemdError: *DenialError becomes code:6 (systemd_denied) with a
// linger suggestion; anything else is code:5 (external-process-failure).
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
