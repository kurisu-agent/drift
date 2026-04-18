package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/kurisu-agent/drift/internal/devpod"
	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/wire"
)

// RegisterKartLifecycle wires the Phase 9 kart lifecycle handlers into reg.
// It is a separate entry point from [RegisterKart] so Phase 7 (list/info) and
// Phase 9 (start/stop/restart/delete/logs) can land and be tested in
// isolation without touching each other's Register call.
func RegisterKartLifecycle(reg *rpc.Registry, d KartDeps) {
	reg.Register(wire.MethodKartStart, d.kartStartHandler)
	reg.Register(wire.MethodKartStop, d.kartStopHandler)
	reg.Register(wire.MethodKartRestart, d.kartRestartHandler)
	reg.Register(wire.MethodKartDelete, d.kartDeleteHandler)
	reg.Register(wire.MethodKartLogs, d.kartLogsHandler)
}

// KartLifecycleParams is the shared param shape for the verbs that only need
// to identify a kart by name.
type KartLifecycleParams struct {
	Name string `json:"name"`
}

// KartLifecycleResult is the envelope returned by start/stop/restart/delete.
// The `status` field is the kart's post-operation status — running for start
// and restart, stopped for stop, not_found for delete. Keeping the shape the
// same across verbs lets drift's client layer parse one result type.
type KartLifecycleResult struct {
	Name   string        `json:"name"`
	Status devpod.Status `json:"status"`
}

// KartLogsResult is returned by kart.logs. The chunk is the raw bytes of
// `devpod logs <name>` captured in one read; streaming is deferred.
type KartLogsResult struct {
	Name  string `json:"name"`
	Chunk string `json:"chunk"`
}

// kartStartHandler runs `devpod up <name>`. Idempotent: starting a running
// kart is a success (code 0).
func (d KartDeps) kartStartHandler(ctx context.Context, params json.RawMessage) (any, error) {
	p, err := bindLifecycleParams(params, "kart.start")
	if err != nil {
		return nil, err
	}
	if err := d.requireDevpod(); err != nil {
		return nil, err
	}
	if _, err := d.Devpod.Up(ctx, devpod.UpOpts{Name: p.Name}); err != nil {
		return nil, rpcerr.New(rpcerr.CodeDevpod, rpcerr.TypeDevpodUpFailed,
			"devpod up %s failed: %v", p.Name, err).Wrap(err).With("kart", p.Name)
	}
	return KartLifecycleResult{Name: p.Name, Status: d.statusFor(ctx, p.Name)}, nil
}

// kartStopHandler runs `devpod stop <name>`. Idempotent: stopping an
// already-stopped kart returns success.
func (d KartDeps) kartStopHandler(ctx context.Context, params json.RawMessage) (any, error) {
	p, err := bindLifecycleParams(params, "kart.stop")
	if err != nil {
		return nil, err
	}
	if err := d.requireDevpod(); err != nil {
		return nil, err
	}
	if err := d.Devpod.Stop(ctx, p.Name); err != nil {
		return nil, rpcerr.New(rpcerr.CodeDevpod, rpcerr.TypeDevpodUnreachable,
			"devpod stop %s failed: %v", p.Name, err).Wrap(err).With("kart", p.Name)
	}
	return KartLifecycleResult{Name: p.Name, Status: d.statusFor(ctx, p.Name)}, nil
}

// kartRestartHandler stops then starts. A stop error is surfaced as-is; once
// stop succeeds, start runs even if the kart was already stopped (idempotent).
func (d KartDeps) kartRestartHandler(ctx context.Context, params json.RawMessage) (any, error) {
	p, err := bindLifecycleParams(params, "kart.restart")
	if err != nil {
		return nil, err
	}
	if err := d.requireDevpod(); err != nil {
		return nil, err
	}
	if err := d.Devpod.Stop(ctx, p.Name); err != nil {
		return nil, rpcerr.New(rpcerr.CodeDevpod, rpcerr.TypeDevpodUnreachable,
			"devpod stop %s failed: %v", p.Name, err).Wrap(err).With("kart", p.Name)
	}
	if _, err := d.Devpod.Up(ctx, devpod.UpOpts{Name: p.Name}); err != nil {
		return nil, rpcerr.New(rpcerr.CodeDevpod, rpcerr.TypeDevpodUpFailed,
			"devpod up %s failed: %v", p.Name, err).Wrap(err).With("kart", p.Name)
	}
	return KartLifecycleResult{Name: p.Name, Status: d.statusFor(ctx, p.Name)}, nil
}

// kartDeleteHandler runs `devpod delete --force <name>` and removes the
// garage dir. This is the one lifecycle verb that errors on missing — we
// check both sides (devpod + garage) up front and return `kart_not_found`
// if neither knows about the kart.
func (d KartDeps) kartDeleteHandler(ctx context.Context, params json.RawMessage) (any, error) {
	p, err := bindLifecycleParams(params, "kart.delete")
	if err != nil {
		return nil, err
	}
	if err := d.requireDevpod(); err != nil {
		return nil, err
	}
	workspaces, err := d.listWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	_, inDevpod := findWorkspace(workspaces, p.Name)
	_, inGarage, err := d.readKartConfig(p.Name)
	if err != nil {
		return nil, err
	}
	if !inDevpod && !inGarage {
		return nil, rpcerr.NotFound(rpcerr.TypeKartNotFound,
			"kart %q not found", p.Name).With("kart", p.Name)
	}
	if inDevpod {
		if err := d.Devpod.Delete(ctx, p.Name); err != nil {
			return nil, rpcerr.New(rpcerr.CodeDevpod, rpcerr.TypeDevpodUnreachable,
				"devpod delete %s failed: %v", p.Name, err).Wrap(err).With("kart", p.Name)
		}
	}
	if inGarage {
		if err := d.removeKartDir(p.Name); err != nil {
			return nil, err
		}
	}
	return KartLifecycleResult{Name: p.Name, Status: devpod.StatusNotFound}, nil
}

// kartLogsHandler returns a single chunk of devpod logs output. Streaming is
// deferred — the MVP surface returns whatever `devpod logs <name>` emits in
// one read.
func (d KartDeps) kartLogsHandler(ctx context.Context, params json.RawMessage) (any, error) {
	p, err := bindLifecycleParams(params, "kart.logs")
	if err != nil {
		return nil, err
	}
	if err := d.requireDevpod(); err != nil {
		return nil, err
	}
	workspaces, err := d.listWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	if _, ok := findWorkspace(workspaces, p.Name); !ok {
		return nil, rpcerr.NotFound(rpcerr.TypeKartNotFound,
			"kart %q not found", p.Name).With("kart", p.Name)
	}
	out, err := d.Devpod.Logs(ctx, p.Name)
	if err != nil {
		return nil, rpcerr.New(rpcerr.CodeDevpod, rpcerr.TypeDevpodUnreachable,
			"devpod logs %s failed: %v", p.Name, err).Wrap(err).With("kart", p.Name)
	}
	return KartLogsResult{Name: p.Name, Chunk: string(out)}, nil
}

// bindLifecycleParams decodes params and enforces that a name was provided.
// The method name is threaded into the error message so a user looking at
// stderr can tell which verb complained.
func bindLifecycleParams(params json.RawMessage, method string) (KartLifecycleParams, error) {
	var p KartLifecycleParams
	if err := rpc.BindParams(params, &p); err != nil {
		return p, err
	}
	if p.Name == "" {
		return p, rpcerr.UserError(rpcerr.TypeInvalidFlag, "%s: name is required", method)
	}
	return p, nil
}

// requireDevpod guards against a zero KartDeps — in production the client is
// always wired, but tests that forget to inject one should get a clear error
// instead of a nil-pointer panic.
func (d KartDeps) requireDevpod() error {
	if d.Devpod == nil {
		return rpcerr.Internal("kart: devpod client not configured")
	}
	return nil
}

// removeKartDir deletes garage/karts/<name>/. A missing dir is not an error —
// the caller already established the kart exists somewhere; if devpod had
// the only record we just finished cleaning that up.
func (d KartDeps) removeKartDir(name string) error {
	dir := filepath.Join(d.GarageDir, "karts", name)
	if err := os.RemoveAll(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return rpcerr.New(rpcerr.CodeInternal, rpcerr.TypeGarageWriteDenied,
			"remove %s: %v", dir, err).Wrap(err)
	}
	return nil
}
