package slogfmt_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/kurisu-agent/drift/internal/slogfmt"
)

func fixedTime() time.Time {
	return time.Date(2026, 4, 18, 9, 5, 7, 0, time.UTC)
}

func TestEmit_HeaderAndAttrs(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	rec := slogfmt.Record{
		Time:  fixedTime(),
		Level: "info",
		Msg:   "kart started",
		Attrs: map[string]any{"kart": "alpha", "pid": 42},
	}
	if !slogfmt.Emit(&buf, rec, slog.LevelInfo) {
		t.Fatal("Emit returned false for level == min")
	}
	want := "09:05:07 INFO  kart started\n" +
		"  kart: alpha\n" +
		"  pid: 42\n"
	if got := buf.String(); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestEmit_LevelPaddingAndCasing(t *testing.T) {
	t.Parallel()
	cases := []struct {
		level string
		head  string
	}{
		{"debug", "DEBUG"},
		{"info", "INFO "},
		{"warn", "WARN "},
		{"error", "ERROR"},
		{"", "INFO "}, // default when missing
	}
	for _, tc := range cases {
		t.Run(tc.level, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			slogfmt.Emit(&buf, slogfmt.Record{Time: fixedTime(), Level: tc.level, Msg: "m"}, slog.LevelDebug)
			if got, want := buf.String(), "09:05:07 "+tc.head+" m\n"; got != want {
				t.Errorf("got %q, want %q", got, want)
			}
		})
	}
}

func TestEmit_FiltersBelowMinLevel(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	rec := slogfmt.Record{Time: fixedTime(), Level: "debug", Msg: "chatty"}
	if slogfmt.Emit(&buf, rec, slog.LevelInfo) {
		t.Fatal("Emit returned true for record below min level")
	}
	if buf.Len() != 0 {
		t.Errorf("buf = %q, want empty", buf.String())
	}
}

func TestEmit_SortsAttrsDeterministically(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	rec := slogfmt.Record{
		Time:  fixedTime(),
		Level: "info",
		Msg:   "m",
		Attrs: map[string]any{"z": 1, "a": 2, "m": 3},
	}
	slogfmt.Emit(&buf, rec, slog.LevelInfo)
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("line count = %d, want 4", len(lines))
	}
	if !strings.HasPrefix(lines[1], "  a:") || !strings.HasPrefix(lines[2], "  m:") || !strings.HasPrefix(lines[3], "  z:") {
		t.Errorf("attrs not sorted:\n%s", buf.String())
	}
}

func TestEmit_MultilineValueIndents(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	rec := slogfmt.Record{
		Time:  fixedTime(),
		Level: "info",
		Msg:   "m",
		Attrs: map[string]any{"trace": "line1\nline2\nline3"},
	}
	slogfmt.Emit(&buf, rec, slog.LevelInfo)
	// Continuation lines indent to "  trace: " = 2 + 5 + 2 = 9 spaces.
	want := "09:05:07 INFO  m\n" +
		"  trace: line1\n" +
		"         line2\n" +
		"         line3\n"
	if got := buf.String(); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestEmit_ZeroTimeRendersPlaceholder(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	slogfmt.Emit(&buf, slogfmt.Record{Level: "info", Msg: "m"}, slog.LevelInfo)
	if got, want := buf.String(), "--:--:-- INFO  m\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestParseLevel(t *testing.T) {
	t.Parallel()
	cases := map[string]slog.Level{
		"":        slog.LevelInfo,
		"debug":   slog.LevelDebug,
		"DEBUG":   slog.LevelDebug,
		"info":    slog.LevelInfo,
		"warn":    slog.LevelWarn,
		"WARNING": slog.LevelWarn,
		"error":   slog.LevelError,
		"err":     slog.LevelError,
		"garbage": slog.LevelInfo,
	}
	for in, want := range cases {
		if got := slogfmt.ParseLevel(in); got != want {
			t.Errorf("ParseLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestDecodeRecord_CanonicalFields(t *testing.T) {
	t.Parallel()
	raw := map[string]any{
		"time":  "2026-04-18T09:05:07Z",
		"level": "WARN",
		"msg":   "slow query",
		"kart":  "alpha",
		"dur":   0.42,
	}
	rec := slogfmt.DecodeRecord(raw)
	if !rec.Time.Equal(fixedTime()) {
		t.Errorf("time = %v, want %v", rec.Time, fixedTime())
	}
	if rec.Level != "WARN" || rec.Msg != "slow query" {
		t.Errorf("got level=%q msg=%q", rec.Level, rec.Msg)
	}
	if rec.Attrs["kart"] != "alpha" || rec.Attrs["dur"] != 0.42 {
		t.Errorf("attrs = %v, want kart=alpha dur=0.42", rec.Attrs)
	}
	if _, ok := rec.Attrs["time"]; ok {
		t.Error("canonical field 'time' leaked into Attrs")
	}
}

func TestDecodeRecord_MissingFieldsAreTolerated(t *testing.T) {
	t.Parallel()
	rec := slogfmt.DecodeRecord(map[string]any{"msg": "bare"})
	if !rec.Time.IsZero() || rec.Level != "" || rec.Msg != "bare" {
		t.Errorf("got %+v, want only msg set", rec)
	}
}
