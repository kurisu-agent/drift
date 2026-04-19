// Package slogfmt is the shared formatter for structured log records. It
// mirrors internal/cli/errfmt so logs and errors share one visual grammar.
// Server code also uses ParseLevel/DecodeRecord for filter pushdown.
package slogfmt

import (
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/kurisu-agent/drift/internal/cli/style"
)

type Record struct {
	Time  time.Time
	Level string
	Msg   string
	Attrs map[string]any
}

// ParseLevel: unknown/empty returns LevelInfo rather than silently
// dropping records below the default threshold.
func ParseLevel(s string) slog.Level {
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

// DecodeRecord parses a slog.JSONHandler line. Unknown fields become
// Attrs so server-side slog.With context survives the round-trip.
func DecodeRecord(raw map[string]any) Record {
	r := Record{}
	if v, ok := raw["time"].(string); ok {
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			r.Time = t
		}
	}
	if v, ok := raw["level"].(string); ok {
		r.Level = v
	}
	if v, ok := raw["msg"].(string); ok {
		r.Msg = v
	}
	for k, v := range raw {
		if k == "time" || k == "level" || k == "msg" {
			continue
		}
		if r.Attrs == nil {
			r.Attrs = make(map[string]any, len(raw))
		}
		r.Attrs[k] = v
	}
	return r
}

// Emit renders:
//
//	HH:MM:SS LEVEL  <msg>
//	  key: value
//
// Attributes sorted by key for deterministic output. Returns false when
// the record is below min so callers can count filtered records.
//
// When w is a TTY (and NO_COLOR is unset) the timestamp is dim, DEBUG is
// dim, WARN yellow, ERROR red, INFO default. Non-TTY writers (tests, pipes,
// CI) get plain ASCII so grep/jq stay clean.
func Emit(w io.Writer, rec Record, min slog.Level) bool {
	if ParseLevel(rec.Level) < min {
		return false
	}
	level := strings.ToUpper(strings.TrimSpace(rec.Level))
	if level == "" {
		level = "INFO"
	}
	stamp := "--:--:--"
	if !rec.Time.IsZero() {
		stamp = rec.Time.Format("15:04:05")
	}
	p := style.For(w, false)
	// Pad the level tag before styling so widths (5 chars) stay consistent
	// whether or not ANSI is emitted.
	paddedLevel := fmt.Sprintf("%-5s", level)
	fmt.Fprintf(w, "%s %s %s\n", p.Dim(stamp), styleLevel(p, level, paddedLevel), rec.Msg)
	if len(rec.Attrs) == 0 {
		return true
	}
	keys := make([]string, 0, len(rec.Attrs))
	for k := range rec.Attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		writeAttr(w, k, rec.Attrs[k])
	}
	return true
}

func styleLevel(p *style.Palette, level, padded string) string {
	switch level {
	case "DEBUG":
		return p.Dim(padded)
	case "WARN":
		return p.Warn(padded)
	case "ERROR":
		return p.Error(padded)
	default:
		return padded
	}
}

// writeAttr indents continuation lines so multi-line values align under
// the first line's value column.
func writeAttr(w io.Writer, key string, val any) {
	s := fmt.Sprintf("%v", val)
	if !strings.Contains(s, "\n") {
		fmt.Fprintf(w, "  %s: %s\n", key, s)
		return
	}
	prefix := strings.Repeat(" ", len("  ")+len(key)+len(": "))
	lines := strings.Split(s, "\n")
	fmt.Fprintf(w, "  %s: %s\n", key, lines[0])
	for _, line := range lines[1:] {
		fmt.Fprintf(w, "%s%s\n", prefix, line)
	}
}
