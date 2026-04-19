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
	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/slogfmt"
	"github.com/kurisu-agent/drift/internal/wire"
)

// The one-shot SSH channel can't stream, so cap unbounded log responses.
// Users who want more can page with --since or set --tail explicitly.
const defaultLogTailLimit = 1000

func RegisterKartLifecycle(reg *rpc.Registry, d KartDeps) {
	reg.Register(wire.MethodKartStart, d.kartStartHandler)
	reg.Register(wire.MethodKartStop, d.kartStopHandler)
	reg.Register(wire.MethodKartRestart, d.kartRestartHandler)
	reg.Register(wire.MethodKartDelete, d.kartDeleteHandler)
	reg.Register(wire.MethodKartLogs, d.kartLogsHandler)
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
	if _, err := d.Devpod.Up(ctx, devpod.UpOpts{Name: p.Name}); err != nil {
		return nil, rpcerr.New(rpcerr.CodeDevpod, rpcerr.TypeDevpodUpFailed,
			"devpod up %s failed: %v", p.Name, err).Wrap(err).With("kart", p.Name)
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
		return nil, rpcerr.New(rpcerr.CodeDevpod, rpcerr.TypeDevpodUnreachable,
			"devpod stop %s failed: %v", p.Name, err).Wrap(err).With("kart", p.Name)
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
		return nil, rpcerr.New(rpcerr.CodeDevpod, rpcerr.TypeDevpodUnreachable,
			"devpod logs %s failed: %v", p.Name, err).Wrap(err).With("kart", p.Name)
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
