package drift

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/kurisu-agent/drift/internal/slogfmt"
	"github.com/kurisu-agent/drift/internal/wire"
)

// Local mirrors keep the CLI off a compile-time dep on internal/server.
type logsParams struct {
	Name  string        `json:"name"`
	Tail  int           `json:"tail,omitempty"`
	Since time.Duration `json:"since,omitempty"`
	Level string        `json:"level,omitempty"`
	Grep  string        `json:"grep,omitempty"`
}

type logsResult struct {
	Name   string   `json:"name"`
	Format string   `json:"format"`
	Lines  []string `json:"lines"`
}

const (
	logFormatJSONL = "jsonl"
	logFormatText  = "text"
)

// logsCmd pushes filters down to the server so the one-shot response
// stays small. Streaming (`-f`) is deferred.
type logsCmd struct {
	Name  string        `arg:"" help:"Kart name."`
	Tail  int           `name:"tail" short:"n" help:"Show the last N lines (0 = server default)."`
	Since time.Duration `name:"since" help:"Only records newer than duration (e.g. 10m, 1h). JSONL only."`
	Level string        `name:"level" help:"Minimum level (debug|info|warn|error). JSONL only."`
	Grep  string        `name:"grep" help:"Substring match on msg (JSONL) or raw line (text)."`
}

func runKartLogs(ctx context.Context, io IO, root *CLI, cmd logsCmd, deps deps) int {
	circuit, err := resolveCircuit(root, deps)
	if err != nil {
		return emitError(io, err)
	}
	params := logsParams(cmd)
	var raw json.RawMessage
	if err := deps.call(ctx, circuit, wire.MethodKartLogs, params, &raw); err != nil {
		return emitError(io, err)
	}
	if root != nil && root.Output == "json" {
		fmt.Fprintln(io.Stdout, string(raw))
		return 0
	}
	var res logsResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return emitError(io, err)
	}
	renderLogs(io.Stdout, res, renderLogLevel(root, cmd.Level), time.Now)
	return 0
}

// renderLogLevel precedence: --level > --debug > default Info. Info is
// the default so server-captured Debug records don't spam normal users.
func renderLogLevel(root *CLI, flagLevel string) slog.Level {
	if flagLevel != "" {
		return slogfmt.ParseLevel(flagLevel)
	}
	if root != nil && root.Debug {
		return slog.LevelDebug
	}
	return slog.LevelInfo
}

// renderLogs wraps text lines into synthetic INFO records with the
// current wall clock — the server has no per-line emission time for
// unstructured sources.
func renderLogs(w stdoutWriter, res logsResult, min slog.Level, now func() time.Time) {
	for _, line := range res.Lines {
		switch res.Format {
		case logFormatJSONL:
			var raw map[string]any
			if err := json.Unmarshal([]byte(line), &raw); err == nil {
				slogfmt.Emit(w, slogfmt.DecodeRecord(raw), min)
				continue
			}
			// Bad JSONL line — render as text so the user still sees it
			// instead of silently dropping.
			slogfmt.Emit(w, slogfmt.Record{Time: now(), Level: "info", Msg: line}, min)
		default:
			slogfmt.Emit(w, slogfmt.Record{Time: now(), Level: "info", Msg: line}, min)
		}
	}
}

// stdoutWriter avoids pulling io into renderLogs's signature.
type stdoutWriter interface {
	Write([]byte) (int, error)
}
