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

// defaultLogTailLimit caps the number of lines returned when the caller does
// not set Tail explicitly. The one-shot SSH channel can't stream, so an
// unbounded response is a foot-gun — users who need more can page with
// --since or set --tail explicitly.
const defaultLogTailLimit = 1000

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

// KartLogsParams is the param shape for kart.logs. Every filter is optional:
// a zero value means "no filter". Tail=0 invokes the server-side default cap.
type KartLogsParams struct {
	Name  string        `json:"name"`
	Tail  int           `json:"tail,omitempty"`
	Since time.Duration `json:"since,omitempty"`
	Level string        `json:"level,omitempty"`
	Grep  string        `json:"grep,omitempty"`
}

// KartLogsResult is returned by kart.logs. Format discriminates between
// JSONL-per-line (each entry is a slog record as an object) and text (each
// entry is a raw line). The client renders both with slogfmt.Emit; text
// lines are wrapped into synthetic INFO records at render time.
type KartLogsResult struct {
	Name   string   `json:"name"`
	Format string   `json:"format"`
	Lines  []string `json:"lines"`
}

// Log format discriminators.
const (
	LogFormatJSONL = "jsonl"
	LogFormatText  = "text"
)

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

// kartLogsHandler fetches `devpod logs <name>` output and packages it into a
// KartLogsResult. If every non-empty line parses as a slog-JSON record with a
// `time` field, Format is "jsonl" and lines pass through verbatim; otherwise
// Format is "text" and the client wraps each line at render time.
//
// Filter order: since → level → grep → tail. The first three are applied
// only where meaningful for the chosen format (since/level require JSONL,
// grep works on both). Tail is applied last so a user asking for "the last
// 20 matching warnings" gets exactly that.
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

// classifyLogLines splits chunk into lines, dropping the trailing empty
// fragment produced by a terminal newline. Every non-empty line must parse
// as a JSON object with a `time` field for the result to be tagged "jsonl";
// otherwise "text" so the client knows to synthesize INFO records.
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

// filterLogLines applies the KartLogsParams filters in order. The since/
// level filters are meaningful only when format is JSONL — for text lines
// the server has no per-line time or level to inspect. Tail is applied last
// so it always reflects the post-filter population.
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
				// Shouldn't happen — classifyLogLines already validated — but
				// guard anyway so a malformed line doesn't panic.
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
