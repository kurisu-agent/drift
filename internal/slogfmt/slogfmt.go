// Package slogfmt is the shared formatter for structured log records. It
// mirrors internal/cli/errfmt: a single [Emit] entry point produces the
// same visual grammar (header line, indented sorted attrs) so logs and
// errors look the same when they show up next to each other. Server code
// also uses the parsing helpers (ParseLevel, DecodeRecord) for filter
// pushdown; only [Emit] is rendering.
package slogfmt

import (
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"time"
)

// Record is the decoded form of one log line. Attrs carries everything that
// isn't one of the canonical slog fields; nil is fine.
type Record struct {
	Time  time.Time
	Level string
	Msg   string
	Attrs map[string]any
}

// ParseLevel maps the common string spellings to slog.Level. Unknown values
// (including the empty string) return slog.LevelInfo so a missing or garbled
// level doesn't silently drop records below the default threshold.
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

// DecodeRecord parses one JSONL line produced by a slog.JSONHandler into a
// Record. Unknown fields become entries in Attrs so a server that adds
// context via slog.With survives the round-trip.
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

// Emit writes rec to w in the standard grammar:
//
//	HH:MM:SS LEVEL  <msg>
//	  key: value
//	  key: value
//
// Levels are padded to 5 characters. Attributes are sorted by key for
// deterministic output. Multi-line values get their continuation lines
// indented to align under the value column.
//
// If ParseLevel(rec.Level) is below min, nothing is written and Emit returns
// false so callers can count filtered records.
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
	fmt.Fprintf(w, "%s %-5s %s\n", stamp, level, rec.Msg)
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

// writeAttr renders a single `  key: value` pair, indenting continuation
// lines so multi-line values align under the first line's value column.
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
