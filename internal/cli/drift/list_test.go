package drift

import (
	"bytes"
	"strings"
	"testing"

	"github.com/kurisu-agent/drift/internal/cli/style"
)

func paletteForTest() *style.Palette {
	return style.For(&bytes.Buffer{}, true)
}

func TestFormatPATListCell(t *testing.T) {
	t.Parallel()
	intp := func(i int) *int { return &i }
	cases := []struct {
		name     string
		in       *listEntryPAT
		wantCell string
		wantWarn bool
	}{
		{name: "nil → empty cell", in: nil, wantCell: "", wantWarn: false},
		{name: "no expiry", in: &listEntryPAT{Slug: "ok"}, wantCell: "ok", wantWarn: false},
		{name: "healthy 30d", in: &listEntryPAT{Slug: "ok", DaysRemaining: intp(30)}, wantCell: "ok", wantWarn: false},
		{name: "boundary 14d → warn", in: &listEntryPAT{Slug: "soon", DaysRemaining: intp(14)}, wantCell: "soon ⚠ 14d", wantWarn: true},
		{name: "1d remaining", in: &listEntryPAT{Slug: "soon", DaysRemaining: intp(1)}, wantCell: "soon ⚠ 1d", wantWarn: true},
		{name: "today", in: &listEntryPAT{Slug: "today", DaysRemaining: intp(0)}, wantCell: "today ⚠ 0d", wantWarn: true},
		{name: "expired", in: &listEntryPAT{Slug: "old", DaysRemaining: intp(-3)}, wantCell: "old ✗ expired", wantWarn: true},
		{name: "missing slug", in: &listEntryPAT{Slug: "ghost", Missing: true}, wantCell: "ghost ✗ missing", wantWarn: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cell, warn := formatPATListCell(tc.in)
			if cell != tc.wantCell {
				t.Errorf("cell: got %q, want %q", cell, tc.wantCell)
			}
			if warn != tc.wantWarn {
				t.Errorf("warn: got %v, want %v", warn, tc.wantWarn)
			}
		})
	}
}

func TestFormatPATInfoRowMissingShowsHint(t *testing.T) {
	t.Parallel()
	got := formatPATInfoRow(&listEntryPAT{Slug: "ghost", Missing: true}, paletteForTest())
	if !strings.Contains(got, "ghost") || !strings.Contains(got, "missing") {
		t.Fatalf("missing PAT info row should mention slug and 'missing'; got %q", got)
	}
}
