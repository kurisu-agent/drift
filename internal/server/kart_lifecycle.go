package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kurisu-agent/drift/internal/devpod"
	driftexec "github.com/kurisu-agent/drift/internal/exec"
	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/slogfmt"
	"github.com/kurisu-agent/drift/internal/wire"
)

// wrapDevpod wraps a devpod-originating error with rpcerr, attaching the
// captured stderr tail (if any) so the client can surface the real cause
// instead of devpod's first-line summary.
func wrapDevpod(code rpcerr.Code, typ rpcerr.Type, kart string, err error, format string, args ...any) *rpcerr.Error {
	re := rpcerr.New(code, typ, format, args...).Wrap(err).With("kart", kart)
	if tail := driftexec.StderrTail(err); tail != "" {
		re = re.With(rpcerr.DataKeyDevpodStderr, tail)
	}
	return re
}

// The one-shot SSH channel can't stream, so cap unbounded log responses.
// Users who want more can page with --since or set --tail explicitly.
const defaultLogTailLimit = 1000

func RegisterKartLifecycle(reg *rpc.Registry, d KartDeps) {
	reg.Register(wire.MethodKartStart, d.kartStartHandler)
	reg.Register(wire.MethodKartStop, d.kartStopHandler)
	reg.Register(wire.MethodKartRestart, d.kartRestartHandler)
	reg.Register(wire.MethodKartDelete, d.kartDeleteHandler)
	reg.Register(wire.MethodKartLogs, d.kartLogsHandler)
	reg.Register(wire.MethodKartSessionEnv, d.kartSessionEnvHandler)
}

type KartLifecycleParams struct {
	Name string `json:"name"`
}

// KartLifecycleResult is shared across start/stop/restart/delete so drift's
// client layer parses one result type. Status reflects post-op state.
type KartLifecycleResult struct {
	Name   string        `json:"name"`
	Status devpod.Status `json:"status"`
}

// KartLogsParams: every filter is optional; Tail=0 uses the server-side cap.
type KartLogsParams struct {
	Name  string        `json:"name"`
	Tail  int           `json:"tail,omitempty"`
	Since time.Duration `json:"since,omitempty"`
	Level string        `json:"level,omitempty"`
	Grep  string        `json:"grep,omitempty"`
}

// KartLogsResult.Format: "jsonl" — each line is a slog record object; "text"
// — raw lines. The client wraps text lines into synthetic INFO records.
type KartLogsResult struct {
	Name   string   `json:"name"`
	Format string   `json:"format"`
	Lines  []string `json:"lines"`
}

const (
	LogFormatJSONL = "jsonl"
	LogFormatText  = "text"
)

func (d KartDeps) kartStartHandler(ctx context.Context, params json.RawMessage) (any, error) {
	p, err := bindLifecycleParams(params, "kart.start")
	if err != nil {
		return nil, err
	}
	if err := d.requireDevpod(); err != nil {
		return nil, err
	}
	setEnv, err := d.workspaceSetEnv(p.Name)
	if err != nil {
		return nil, err
	}
	if _, err := d.Devpod.Up(ctx, devpod.UpOpts{Name: p.Name, SetEnv: setEnv}); err != nil {
		return nil, wrapDevpod(rpcerr.CodeDevpod, rpcerr.TypeDevpodUpFailed, p.Name, err,
			"devpod up %s failed: %v", p.Name, err)
	}
	return KartLifecycleResult{Name: p.Name, Status: d.statusFor(ctx, p.Name)}, nil
}

func (d KartDeps) kartStopHandler(ctx context.Context, params json.RawMessage) (any, error) {
	p, err := bindLifecycleParams(params, "kart.stop")
	if err != nil {
		return nil, err
	}
	if err := d.requireDevpod(); err != nil {
		return nil, err
	}
	if err := d.Devpod.Stop(ctx, p.Name); err != nil {
		return nil, wrapDevpod(rpcerr.CodeDevpod, rpcerr.TypeDevpodUnreachable, p.Name, err,
			"devpod stop %s failed: %v", p.Name, err)
	}
	return KartLifecycleResult{Name: p.Name, Status: d.statusFor(ctx, p.Name)}, nil
}

func (d KartDeps) kartRestartHandler(ctx context.Context, params json.RawMessage) (any, error) {
	p, err := bindLifecycleParams(params, "kart.restart")
	if err != nil {
		return nil, err
	}
	if err := d.requireDevpod(); err != nil {
		return nil, err
	}
	// Resolve env BEFORE the stop so a chest miss fails fast without
	// leaving the kart stopped with no re-up coming.
	setEnv, err := d.workspaceSetEnv(p.Name)
	if err != nil {
		return nil, err
	}
	if err := d.Devpod.Stop(ctx, p.Name); err != nil {
		return nil, wrapDevpod(rpcerr.CodeDevpod, rpcerr.TypeDevpodUnreachable, p.Name, err,
			"devpod stop %s failed: %v", p.Name, err)
	}
	if _, err := d.Devpod.Up(ctx, devpod.UpOpts{Name: p.Name, SetEnv: setEnv}); err != nil {
		return nil, wrapDevpod(rpcerr.CodeDevpod, rpcerr.TypeDevpodUpFailed, p.Name, err,
			"devpod up %s failed: %v", p.Name, err)
	}
	return KartLifecycleResult{Name: p.Name, Status: d.statusFor(ctx, p.Name)}, nil
}

// kartDeleteHandler is the one lifecycle verb that errors on missing; both
// sides (devpod + garage) are checked up front.
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
			return nil, wrapDevpod(rpcerr.CodeDevpod, rpcerr.TypeDevpodUnreachable, p.Name, err,
				"devpod delete %s failed: %v", p.Name, err)
		}
	}
	if inGarage {
		if err := d.removeKartDir(p.Name); err != nil {
			return nil, err
		}
	}
	return KartLifecycleResult{Name: p.Name, Status: devpod.StatusNotFound}, nil
}

// kartLogsHandler. Filter order: since → level → grep → tail. since/level
// require JSONL (for the record fields to inspect); grep works on both.
// Tail is last so "last 20 matching warnings" means exactly that.
func (d KartDeps) kartLogsHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p KartLogsParams
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	if p.Name == "" {
		return nil, rpcerr.UserError(rpcerr.TypeInvalidFlag, "kart.logs: name is required")
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
		return nil, wrapDevpod(rpcerr.CodeDevpod, rpcerr.TypeDevpodUnreachable, p.Name, err,
			"devpod logs %s failed: %v", p.Name, err)
	}
	format, lines := classifyLogLines(string(out))
	lines = filterLogLines(lines, format, p, time.Now())
	return KartLogsResult{Name: p.Name, Format: format, Lines: lines}, nil
}

// classifyLogLines tags output as "jsonl" iff every non-empty line parses as
// an object with a `time` field.
func classifyLogLines(chunk string) (format string, lines []string) {
	if chunk == "" {
		return LogFormatText, nil
	}
	split := strings.Split(strings.TrimSuffix(chunk, "\n"), "\n")
	allJSONL := true
	nonEmpty := 0
	for _, line := range split {
		if line == "" {
			continue
		}
		nonEmpty++
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			allJSONL = false
			break
		}
		if _, ok := obj["time"]; !ok {
			allJSONL = false
			break
		}
	}
	if nonEmpty == 0 {
		return LogFormatText, nil
	}
	if allJSONL {
		return LogFormatJSONL, split
	}
	return LogFormatText, split
}

func filterLogLines(lines []string, format string, p KartLogsParams, now time.Time) []string {
	minLevel := slogfmt.ParseLevel(p.Level)
	hasLevel := strings.TrimSpace(p.Level) != ""
	cutoff := time.Time{}
	if p.Since > 0 {
		cutoff = now.Add(-p.Since)
	}
	grep := p.Grep

	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		if format == LogFormatJSONL {
			var obj map[string]any
			if err := json.Unmarshal([]byte(line), &obj); err != nil {
				// classifyLogLines already validated — guard against a
				// malformed line slipping through rather than panicking.
				continue
			}
			rec := slogfmt.DecodeRecord(obj)
			if !cutoff.IsZero() && rec.Time.Before(cutoff) {
				continue
			}
			if hasLevel && slogfmt.ParseLevel(rec.Level) < minLevel {
				continue
			}
			if grep != "" && !strings.Contains(rec.Msg, grep) {
				continue
			}
		} else {
			if grep != "" && !strings.Contains(line, grep) {
				continue
			}
		}
		out = append(out, line)
	}

	limit := p.Tail
	if limit <= 0 {
		limit = defaultLogTailLimit
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

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

func (d KartDeps) requireDevpod() error {
	if d.Devpod == nil {
		return rpcerr.Internal("kart: devpod client not configured")
	}
	return nil
}

// KartSessionEnvResult is the response for kart.session_env — returns
// resolved KEY=VALUE pairs the client appends to the remote devpod ssh
// invocation as --set-env flags. Empty Env means nothing to inject.
type KartSessionEnvResult struct {
	Name string   `json:"name"`
	Env  []string `json:"env"`
}

// kartSessionEnvHandler re-resolves env.session from chest on every call
// so rotated secrets land on the next `drift connect`. Values never
// persist on the client — caller appends them to the ssh command and
// lets the ssh channel carry them to the circuit.
func (d KartDeps) kartSessionEnvHandler(_ context.Context, params json.RawMessage) (any, error) {
	p, err := bindLifecycleParams(params, "kart.session_env")
	if err != nil {
		return nil, err
	}
	cfg, ok, err := d.readKartConfig(p.Name)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, rpcerr.NotFound(rpcerr.TypeKartNotFound,
			"kart %q not found", p.Name).With("kart", p.Name)
	}
	if len(cfg.Env.Session) == 0 {
		return KartSessionEnvResult{Name: p.Name, Env: []string{}}, nil
	}
	resolved, err := d.resolveEnvBlock("session", cfg.Env.Session)
	if err != nil {
		return nil, err
	}
	return KartSessionEnvResult{Name: p.Name, Env: envKVPairs(resolved)}, nil
}

// workspaceSetEnv re-reads chest-backed workspace env for a kart so
// start / restart pick up rotated secrets. Missing kart config, empty
// env block, or no chest wiring return (nil, nil) — the caller just
// omits SetEnv from UpOpts.
func (d KartDeps) workspaceSetEnv(name string) ([]string, error) {
	cfg, ok, err := d.readKartConfig(name)
	if err != nil {
		return nil, err
	}
	if !ok || len(cfg.Env.Workspace) == 0 {
		return nil, nil
	}
	resolved, err := d.resolveEnvBlock("workspace", cfg.Env.Workspace)
	if err != nil {
		return nil, err
	}
	return envKVPairs(resolved), nil
}

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
