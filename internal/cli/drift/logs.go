package drift

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	charmlog "github.com/charmbracelet/log"
	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/wire"
)

// Local mirror keeps the CLI off a compile-time dep on internal/server.
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
	_, circuit, err := resolveCircuit(root, deps)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	var raw json.RawMessage
	if err := deps.call(ctx, circuit, wire.MethodKartLogs, cmd, &raw); err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	if root != nil && root.Output == "json" {
		fmt.Fprintln(io.Stdout, string(raw))
		return 0
	}
	var res logsResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	renderLogs(io.Stdout, res, parseLogLevel(renderLogLevelString(root, cmd.Level)), time.Now)
	return 0
}

// renderLogLevelString resolves the effective minimum level string. Same
// precedence as before: --level > --debug > info default.
func renderLogLevelString(root *CLI, flagLevel string) string {
	if flagLevel != "" {
		return flagLevel
	}
	if root != nil && root.Debug {
		return "debug"
	}
	return "info"
}

// parseLogLevel mirrors the old slogfmt.ParseLevel: unknown / empty
// resolves to Info so records below the default never silently drop.
func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error", "err":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// decodeLogRecord parses one slog.JSONHandler line. Unknown keys land in
// attrs so server-side slog.With context survives the round trip.
func decodeLogRecord(raw map[string]any) (t time.Time, level, msg string, attrs []any) {
	if v, ok := raw["time"].(string); ok {
		if parsed, err := time.Parse(time.RFC3339Nano, v); err == nil {
			t = parsed
		}
	}
	if v, ok := raw["level"].(string); ok {
		level = v
	}
	if v, ok := raw["msg"].(string); ok {
		msg = v
	}
	for k, v := range raw {
		if k == "time" || k == "level" || k == "msg" {
			continue
		}
		attrs = append(attrs, k, v)
	}
	return
}

// renderLogs feeds JSONL records into a charmlog.Logger and wraps plain
// text lines as Info records with the current wall clock. JSONL records
// route through slog.Handler so the upstream record's Time/Level survive.
func renderLogs(w io.Writer, res logsResult, min slog.Level, now func() time.Time) {
	logger := charmlog.NewWithOptions(w, charmlog.Options{
		ReportTimestamp: true,
		TimeFormat:      "15:04:05",
	})
	logger.SetLevel(slogToCharmLevel(min))

	for _, line := range res.Lines {
		switch res.Format {
		case logFormatJSONL:
			var raw map[string]any
			if err := json.Unmarshal([]byte(line), &raw); err == nil {
				ts, level, msg, attrs := decodeLogRecord(raw)
				if ts.IsZero() {
					ts = now()
				}
				rec := slog.NewRecord(ts, parseLogLevel(level), msg, 0)
				for i := 0; i+1 < len(attrs); i += 2 {
					rec.AddAttrs(slog.Any(fmt.Sprintf("%v", attrs[i]), attrs[i+1]))
				}
				_ = logger.Handle(context.Background(), rec)
				continue
			}
			// Bad JSONL — render as info text so the user still sees it.
			logger.Info(line)
		default:
			logger.Info(line)
		}
	}
}

func slogToCharmLevel(l slog.Level) charmlog.Level {
	switch l {
	case slog.LevelDebug:
		return charmlog.DebugLevel
	case slog.LevelWarn:
		return charmlog.WarnLevel
	case slog.LevelError:
		return charmlog.ErrorLevel
	default:
		return charmlog.InfoLevel
	}
}
